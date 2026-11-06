package ui

import "os"

// palette paints with escape sequences, or not at all under NO_COLOR.
type palette struct {
	fancy, on, truecolor bool
}

func newPalette() palette {
	ct := os.Getenv("COLORTERM")
	return palette{
		fancy:     true,
		on:        os.Getenv("NO_COLOR") == "",
		truecolor: ct == "truecolor" || ct == "24bit" || os.Getenv("WT_SESSION") != "",
	}
}

func (p palette) paint(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (p palette) accent(s string) string {
	if p.truecolor {
		return p.paint("38;2;139;124;246", s)
	}
	return p.paint("38;5;99", s)
}

func (p palette) badge(s string) string {
	if p.truecolor {
		return p.paint("1;38;2;255;255;255;48;2;109;91;208", s)
	}
	return p.paint("1;97;48;5;62", s)
}

func (p palette) green(s string) string   { return p.paint("32", s) }
func (p palette) yellow(s string) string  { return p.paint("33", s) }
func (p palette) red(s string) string     { return p.paint("31", s) }
func (p palette) gray(s string) string    { return p.paint("90", s) }
func (p palette) bold(s string) string    { return p.style("1", s) }
func (p palette) inverse(s string) string { return p.style("7", s) }

func (p palette) style(code, s string) string {
	if !p.fancy || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}
