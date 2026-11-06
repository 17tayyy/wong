package ui

import (
	"fmt"
	"time"
)

var frames = []string{"◒", "◐", "◓", "◑"}

// Spinner turns while something takes a while, with a hint after it, such as the keys that do something meanwhile.
type Spinner struct {
	u         *UI
	msg, hint string
	frame     int
}

// Spin starts a spinner; in plain output it shows nothing until it stops.
func (u *UI) Spin(msg, hint string) *Spinner {
	s := &Spinner{u: u, msg: msg, hint: hint}
	if u.tty == nil {
		return s
	}
	u.mu.Lock()
	u.stopSpin()
	u.gap(true)
	u.spin = s
	s.draw()
	u.mu.Unlock()
	go func() {
		for {
			time.Sleep(90 * time.Millisecond)
			u.mu.Lock()
			if u.spin != s {
				u.mu.Unlock()
				return
			}
			s.draw()
			u.mu.Unlock()
		}
	}()
	return s
}

func (s *Spinner) draw() {
	p := s.u.pal
	cols := s.u.cols()
	msg := []rune(s.msg)
	if len(msg) > cols-5 {
		msg = append(msg[:max(cols-6, 0)], '…')
	}
	line := p.accent(frames[s.frame%len(frames)]) + "  " + string(msg)
	if s.hint != "" && visible(line)+2+len([]rune(s.hint)) < cols {
		line += "  " + p.gray(s.hint)
	}
	s.frame++
	fmt.Fprint(s.u.out, "\r\033[K"+line)
}

// Hint changes the hint after the spinner.
func (s *Spinner) Hint(hint string) {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	s.hint = hint
}

// Stop ends the spinner on a step that says what was done, or takes it away when done is empty.
func (s *Spinner) Stop(done string, details ...string) {
	s.end(func() { s.u.Step(done, details...) }, done)
}

// Fail ends the spinner on a warning.
func (s *Spinner) Fail(problem string, details ...string) {
	s.end(func() { s.u.Warn(problem, details...) }, problem)
}

func (s *Spinner) end(say func(), text string) {
	u := s.u
	u.mu.Lock()
	if u.spin == s {
		u.stopSpin()
	}
	u.mu.Unlock()
	if text != "" {
		say()
	}
}

// stopSpin takes the running spinner's line away.
func (u *UI) stopSpin() {
	if u.spin == nil {
		return
	}
	u.spin = nil
	fmt.Fprint(u.out, "\r\033[K")
}
