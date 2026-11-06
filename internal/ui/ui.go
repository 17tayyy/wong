// Package ui draws wong's guided commands: in a terminal, a frame of steps, boxed notes, prompts and spinners;
// anywhere else, plain lines.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"golang.org/x/term"
)

// ErrCancelled is what a prompt returns when it is left with Esc, or when the input ends.
var ErrCancelled = errors.New("cancelled")

type block int

const (
	none block = iota
	step
	other
)

// UI writes to out, and asks through in when both are a terminal. Close it when done, to give the terminal back.
type UI struct {
	out     io.Writer
	tty     *os.File
	in      *os.File
	pal     palette
	mu      sync.Mutex
	last    block
	spaced  bool
	spin    *Spinner
	restore func()
	keys    chan Key
	sigs    chan os.Signal
}

// New draws the frame when out is a terminal that takes escape sequences, and asks when in is one too.
func New(out io.Writer, in io.Reader) *UI {
	u := &UI{out: out}
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) || os.Getenv("TERM") == "dumb" || UnderAgent() || !colorOn(f) {
		return u
	}
	u.tty = f
	u.pal = newPalette()
	if i, ok := in.(*os.File); ok && term.IsTerminal(int(i.Fd())) {
		if restore, err := keysOneByOne(i); err == nil {
			u.in, u.restore, u.keys = i, restore, readKeys(i)
		}
	}
	u.sigs = make(chan os.Signal, 1)
	signal.Notify(u.sigs, os.Interrupt, syscall.SIGTERM)
	go func(sigs chan os.Signal) {
		if _, ok := <-sigs; ok {
			u.quit()
		}
	}(u.sigs)
	fmt.Fprint(f, "\033[?25l")
	return u
}

// UnderAgent reports whether a coding agent runs this command, as the variables Claude Code and others set say: then
// nothing is asked and nothing is drawn, even in a terminal the agent opened.
func UnderAgent() bool {
	return os.Getenv("AI_AGENT") != "" || os.Getenv("CLAUDECODE") != ""
}

// Fancy reports whether the frame is drawn.
func (u *UI) Fancy() bool { return u.tty != nil }

// CanAsk reports whether the prompts work: the frame is drawn and keys come from a terminal.
func (u *UI) CanAsk() bool { return u.in != nil }

// Keys hands over the keys pressed while no prompt reads them; it never sends when the UI cannot ask.
func (u *UI) Keys() <-chan Key { return u.keys }

// Close stops a spinner left running and gives the terminal back as it was.
func (u *UI) Close() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.end()
}

func (u *UI) end() {
	if u.tty == nil {
		return
	}
	u.stopSpin()
	if u.restore != nil {
		u.restore()
		u.restore = nil
	}
	if u.sigs != nil {
		signal.Stop(u.sigs)
		close(u.sigs)
		u.sigs = nil
		fmt.Fprint(u.tty, "\033[?25h")
	}
}

// quit leaves at Ctrl+C, with the terminal given back.
func (u *UI) quit() {
	u.mu.Lock()
	u.stopSpin()
	fmt.Fprint(u.out, "\r\033[K")
	if !u.spaced {
		fmt.Fprint(u.out, u.pal.gray("│")+"\n")
	}
	fmt.Fprint(u.out, u.pal.gray("└")+"  "+u.pal.red("Cancelled")+"\n")
	u.end()
	os.Exit(130)
}

func readKeys(f *os.File) chan Key {
	keys := make(chan Key, 1024)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := f.Read(buf)
			for _, k := range parseKeys(buf[:n]) {
				keys <- k
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()
	return keys
}

func (u *UI) drain() {
	for {
		select {
		case _, ok := <-u.keys:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

func (u *UI) write(s string) {
	fmt.Fprint(u.out, s)
	u.spaced = false
}

// gap puts a bar between blocks; steps that follow one another go without one.
func (u *UI) gap(tight bool) {
	if u.spaced || u.last == none || tight && u.last == step {
		return
	}
	fmt.Fprint(u.out, u.pal.gray("│")+"\n")
	u.spaced = true
}

func (u *UI) cols() int {
	if u.tty != nil {
		if w, _, err := term.GetSize(int(u.tty.Fd())); err == nil && w > 20 {
			return w
		}
	}
	return 80
}

// Intro opens the frame with wong's name and a word after it, such as the version.
func (u *UI) Intro(after string) {
	if u.tty == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.write(u.pal.gray("┌") + "  " + u.pal.badge(" wong ") + "  " + u.pal.gray(after) + "\n")
	u.last = other
}

// Continue goes on with a frame something else opened, such as the install script.
func (u *UI) Continue() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.last = step
}

// Step says what was done, with lines of detail under it.
func (u *UI) Step(title string, details ...string) {
	u.line(func(p palette) string { return p.green("◇") }, title, details)
}

// Warn says what went wrong or needs doing by hand, with lines of detail under it.
func (u *UI) Warn(title string, details ...string) {
	u.line(func(p palette) string { return p.yellow("▲") }, title, details)
}

func (u *UI) line(symbol func(palette) string, title string, details []string) {
	if u.tty == nil {
		fmt.Fprintln(u.out, title)
		for _, d := range details {
			fmt.Fprintln(u.out, "  "+d)
		}
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gap(true)
	width := u.cols() - 3
	bar := u.pal.gray("│") + "  "
	for i, l := range wrap(title, width) {
		if i == 0 {
			u.write(symbol(u.pal) + "  " + l + "\n")
		} else {
			u.write(bar + l + "\n")
		}
	}
	for _, d := range details {
		for _, l := range wrap(d, width) {
			u.write(bar + l + "\n")
		}
	}
	u.last = step
}

// Note draws lines in a box under a title, for what to read before going on, like a code to enter.
func (u *UI) Note(title string, lines ...string) {
	if u.tty == nil {
		fmt.Fprintln(u.out, title)
		for _, l := range lines {
			fmt.Fprintln(u.out, strings.TrimRight("  "+l, " "))
		}
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gap(false)
	cols := u.cols()
	var body []string
	for _, l := range lines {
		body = append(body, wrap(l, min(cols-6, 72))...)
	}
	inner := visible(title) + 2
	for _, l := range body {
		inner = max(inner, visible(l))
	}
	p := u.pal
	if inner+6 > cols {
		u.write(p.green("◇") + "  " + title + "\n")
		for _, l := range body {
			u.write(p.gray("│") + "  " + l + "\n")
		}
		u.last = other
		return
	}
	empty := p.gray("│") + strings.Repeat(" ", inner+4) + p.gray("│") + "\n"
	u.write(p.green("◇") + "  " + title + " " + p.gray(strings.Repeat("─", inner+1-visible(title))+"╮") + "\n" + empty)
	for _, l := range body {
		u.write(p.gray("│") + "  " + l + strings.Repeat(" ", inner-visible(l)) + "  " + p.gray("│") + "\n")
	}
	u.write(empty + p.gray("├"+strings.Repeat("─", inner+4)+"╯") + "\n")
	u.last = other
}

// Fail closes the frame on what stopped the command.
func (u *UI) Fail(msg string) {
	if u.tty == nil {
		fmt.Fprintln(u.out, msg)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stopSpin()
	u.gap(false)
	for i, l := range wrap(msg, u.cols()-3) {
		if i == 0 {
			u.write(u.pal.red("■") + "  " + l + "\n")
		} else {
			u.write(u.pal.gray("│") + "  " + l + "\n")
		}
	}
	u.write(u.pal.gray("└") + "\n")
	u.last = none
}

// Cancelled closes the frame on a command left halfway.
func (u *UI) Cancelled() {
	if u.tty == nil {
		fmt.Fprintln(u.out, "Cancelled")
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stopSpin()
	u.gap(false)
	u.write(u.pal.gray("└") + "  " + u.pal.red("Cancelled") + "\n")
	u.last = none
}

// Outro closes the frame with a last line.
func (u *UI) Outro(msg string) {
	if u.tty == nil {
		fmt.Fprintln(u.out, msg)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gap(false)
	for i, l := range wrap(msg, u.cols()-3) {
		if i == 0 {
			u.write(u.pal.gray("└") + "  " + l + "\n")
		} else {
			u.write("   " + l + "\n")
		}
	}
	u.last = none
}

// Accent paints s in wong's color, for what to look at in a line: a code, an address.
func (u *UI) Accent(s string) string { return u.pal.accent(s) }

// Bold makes s bold.
func (u *UI) Bold(s string) string { return u.pal.bold(s) }

// Dim makes s quieter, for what matters less in a line.
func (u *UI) Dim(s string) string { return u.pal.gray(s) }

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func visible(s string) int {
	return utf8.RuneCountInString(ansi.ReplaceAllString(s, ""))
}

// wrap breaks s at spaces into lines no wider than width, keeping its indent on each.
func wrap(s string, width int) []string {
	if visible(s) <= width || width < 20 {
		return []string{s}
	}
	trimmed := strings.TrimLeft(s, " ")
	indent := s[:len(s)-len(trimmed)]
	var lines []string
	line := indent
	for _, word := range strings.Fields(trimmed) {
		if line != indent && visible(line)+1+visible(word) > width {
			lines = append(lines, line)
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	return append(lines, line)
}
