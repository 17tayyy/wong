package ui

import (
	"bytes"
	"reflect"
	"testing"
)

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("ab\x1b[A\x1b[B\r\n\x7f\x1b\x1b[1;5C\x15é"))
	want := []Key{{Rune: 'a'}, {Rune: 'b'}, {Name: "up"}, {Name: "down"}, {Name: "enter"}, {Name: "backspace"},
		{Name: "esc"}, {Name: "ctrl-u"}, {Rune: 'é'}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseKeys = %+v, want %+v", got, want)
	}
	if got := parseKeys([]byte("\x1b")); !reflect.DeepEqual(got, []Key{{Name: "esc"}}) {
		t.Errorf("a lone escape = %+v", got)
	}
}

func TestWrapKeepsTheIndent(t *testing.T) {
	got := wrap("  one two three four five six seven eight nine ten", 24)
	want := []string{"  one two three four", "  five six seven eight", "  nine ten"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrap = %q, want %q", got, want)
	}
	if got := wrap("\x1b[32mgreen\x1b[0m stays whole", 20); len(got) != 1 {
		t.Errorf("escape sequences count as wide: %q", got)
	}
}

func TestPlainOutputHasNoFrame(t *testing.T) {
	var out bytes.Buffer
	u := New(&out, nil)
	defer u.Close()
	u.Intro("v1")
	u.Step("Imported 2 notes", "detail")
	u.Note("Code", "", "ABCD")
	sp := u.Spin("Waiting", "hint")
	sp.Stop("Approved")
	u.Outro("Done.")
	want := "Imported 2 notes\n  detail\nCode\n\n  ABCD\nApproved\nDone.\n"
	if out.String() != want {
		t.Errorf("plain output = %q, want %q", out.String(), want)
	}
	if u.CanAsk() {
		t.Error("a buffer can ask")
	}
	if _, err := u.Select("Which?", []Option{{Label: "A"}}, 0); err != ErrNoTerminal {
		t.Errorf("Select without a terminal = %v", err)
	}
}
