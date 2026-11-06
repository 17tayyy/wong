package ui

import "unicode/utf8"

// Key is one key pressed: a rune typed, or a named key such as "enter", "up" or "esc".
type Key struct {
	Rune rune
	Name string
}

// parseKeys splits what the terminal sent in one read into keys; a paste arrives as many.
func parseKeys(b []byte) []Key {
	var keys []Key
	for len(b) > 0 {
		switch c := b[0]; {
		case c == 0x1b:
			if len(b) == 1 {
				return append(keys, Key{Name: "esc"})
			}
			n, name := escape(b)
			if name != "" {
				keys = append(keys, Key{Name: name})
			}
			b = b[n:]
			continue
		case c == '\r' || c == '\n':
			keys = append(keys, Key{Name: "enter"})
			if c == '\r' && len(b) > 1 && b[1] == '\n' {
				b = b[1:]
			}
		case c == 0x7f || c == 0x08:
			keys = append(keys, Key{Name: "backspace"})
		case c == '\t':
			keys = append(keys, Key{Name: "tab"})
		case c == 0x03:
			keys = append(keys, Key{Name: "ctrl-c"})
		case c == 0x15:
			keys = append(keys, Key{Name: "ctrl-u"})
		case c == 0x17:
			keys = append(keys, Key{Name: "ctrl-w"})
		case c < 0x20:
		default:
			r, n := utf8.DecodeRune(b)
			if r != utf8.RuneError {
				keys = append(keys, Key{Rune: r})
			}
			b = b[n:]
			continue
		}
		b = b[1:]
	}
	return keys
}

// escape reads an escape sequence at the start of b, and names the arrow keys among them.
func escape(b []byte) (int, string) {
	if b[1] != '[' && b[1] != 'O' {
		return 1, "esc"
	}
	for i := 2; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			name := map[byte]string{'A': "up", 'B': "down", 'C': "right", 'D': "left"}[b[i]]
			if i != 2 {
				name = ""
			}
			return i + 1, name
		}
	}
	return len(b), ""
}
