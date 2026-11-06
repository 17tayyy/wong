package ui

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoTerminal is what a prompt returns when the UI cannot ask.
var ErrNoTerminal = errors.New("not a terminal")

// live is a block that is drawn again in place as keys come, such as a prompt.
type live struct {
	u    *UI
	rows int
	gap  bool
}

func (u *UI) live() *live {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stopSpin()
	l := &live{u: u, gap: !u.spaced && u.last != none}
	return l
}

func (l *live) draw(lines []string) {
	u := l.u
	u.mu.Lock()
	defer u.mu.Unlock()
	if l.gap {
		lines = append([]string{u.pal.gray("│")}, lines...)
	}
	var b strings.Builder
	if l.rows > 0 {
		b.WriteString("\r")
		if l.rows > 1 {
			fmt.Fprintf(&b, "\033[%dA", l.rows-1)
		}
		b.WriteString("\033[J")
	}
	b.WriteString(strings.Join(lines, "\n"))
	fmt.Fprint(u.out, b.String())
	cols := u.cols()
	l.rows = 0
	for _, line := range lines {
		l.rows += max(1, (visible(line)+cols-1)/cols)
	}
}

// done draws the block a last time and leaves it, or takes it away when lines is empty.
func (l *live) done(lines []string) {
	l.draw(lines)
	u := l.u
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(lines) == 0 {
		fmt.Fprint(u.out, "\r\033[K")
		return
	}
	u.write("\n")
	u.last = other
}

func (u *UI) next() (Key, error) {
	k, ok := <-u.keys
	if !ok || k.Name == "ctrl-c" {
		return Key{}, ErrCancelled
	}
	return k, nil
}

// Option is one answer of a Select, with a hint shown while it is the one picked.
type Option struct {
	Label, Hint string
}

// Select asks to pick one option with the arrow keys, starting at initial, and returns its index.
func (u *UI) Select(question string, options []Option, initial int) (int, error) {
	if !u.CanAsk() {
		return 0, ErrNoTerminal
	}
	u.drain()
	p := u.pal
	l := u.live()
	cur := initial
	cols := u.cols()
	for {
		lines := []string{p.accent("◆") + "  " + p.bold(question)}
		for i, o := range options {
			if i == cur {
				line := p.accent("│") + "  " + p.accent("●") + " " + o.Label
				if hint := " " + p.gray("("+o.Hint+")"); o.Hint != "" && visible(line+hint) < cols {
					line += hint
				}
				lines = append(lines, line)
			} else {
				lines = append(lines, p.accent("│")+"  "+p.gray("○ "+o.Label))
			}
		}
		l.draw(append(lines, p.accent("└")))
		k, err := u.next()
		if err != nil || k.Name == "esc" {
			l.done([]string{p.red("■") + "  " + question})
			return 0, ErrCancelled
		}
		switch {
		case k.Name == "up" || k.Rune == 'k':
			cur = (cur + len(options) - 1) % len(options)
		case k.Name == "down" || k.Name == "tab" || k.Rune == 'j':
			cur = (cur + 1) % len(options)
		case k.Rune >= '1' && int(k.Rune-'1') < len(options):
			cur = int(k.Rune - '1')
		case k.Name == "enter":
			l.done([]string{p.green("◇") + "  " + question, p.gray("│") + "  " + p.gray(options[cur].Label)})
			return cur, nil
		}
	}
}

// Confirm asks yes or no, starting at yes when yes is true.
func (u *UI) Confirm(question string, yes bool) (bool, error) {
	if !u.CanAsk() {
		return false, ErrNoTerminal
	}
	u.drain()
	p := u.pal
	l := u.live()
	answer := func(yes bool) string {
		if yes {
			return "Yes"
		}
		return "No"
	}
	for {
		y, n := p.gray("○ Yes"), p.accent("●")+" No"
		if yes {
			y, n = p.accent("●")+" Yes", p.gray("○ No")
		}
		l.draw([]string{p.accent("◆") + "  " + p.bold(question), p.accent("│") + "  " + y + p.gray(" / ") + n, p.accent("└")})
		k, err := u.next()
		if err != nil || k.Name == "esc" {
			l.done([]string{p.red("■") + "  " + question})
			return false, ErrCancelled
		}
		switch {
		case k.Name == "left" || k.Name == "right" || k.Name == "tab" || k.Rune == 'h' || k.Rune == 'l':
			yes = !yes
		case k.Rune == 'y' || k.Rune == 'Y':
			yes = true
		case k.Rune == 'n' || k.Rune == 'N':
			yes = false
		case k.Name == "enter":
			l.done([]string{p.green("◇") + "  " + question, p.gray("│") + "  " + p.gray(answer(yes))})
			return yes, nil
		}
	}
}

// Text is a line to type: what greys out while it is empty, whether to hide it, and what checks it on Enter.
type Text struct {
	Question    string
	Placeholder string
	Hint        string
	Secret      bool
	Check       func(string) error
}

// Ask asks for t's line and returns it trimmed; Esc takes the prompt away and returns ErrCancelled.
func (u *UI) Ask(t Text) (string, error) {
	if !u.CanAsk() {
		return "", ErrNoTerminal
	}
	u.drain()
	p := u.pal
	l := u.live()
	var value []rune
	problem := ""
	shown := func() string {
		if t.Secret {
			return strings.Repeat("•", len(value))
		}
		return string(value)
	}
	for {
		symbol, bar, end := p.accent("◆"), p.accent("│"), p.accent("└")
		if problem != "" {
			symbol, bar, end = p.yellow("▲"), p.yellow("│"), p.yellow("└  "+problem)
		}
		input := shown() + p.inverse(" ")
		if len(value) == 0 && t.Placeholder != "" {
			first, rest := []rune(t.Placeholder)[:1], []rune(t.Placeholder)[1:]
			input = p.inverse(string(first)) + p.gray(string(rest))
		}
		lines := []string{symbol + "  " + p.bold(t.Question)}
		if t.Hint != "" {
			lines = append(lines, bar+"  "+p.gray(t.Hint))
		}
		l.draw(append(lines, bar+"  "+input, end))
		k, err := u.next()
		if err != nil {
			l.done([]string{p.red("■") + "  " + t.Question})
			return "", ErrCancelled
		}
		switch {
		case k.Name == "esc":
			l.done(nil)
			return "", ErrCancelled
		case k.Name == "enter":
			answer := strings.TrimSpace(string(value))
			if t.Check != nil {
				if err := t.Check(answer); err != nil {
					problem = err.Error()
					continue
				}
			}
			l.done([]string{p.green("◇") + "  " + t.Question, p.gray("│") + "  " + p.gray(shown())})
			return answer, nil
		case k.Name == "backspace" && len(value) > 0:
			value = value[:len(value)-1]
		case k.Name == "ctrl-u":
			value = nil
		case k.Name == "ctrl-w":
			s := strings.TrimRight(string(value), " ")
			value = []rune(s[:strings.LastIndex(s, " ")+1])
		case k.Rune != 0:
			value = append(value, k.Rune)
		default:
			continue
		}
		problem = ""
	}
}
