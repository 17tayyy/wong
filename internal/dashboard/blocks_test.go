package dashboard

import (
	"strings"
	"testing"
)

func TestSplitBlocks(t *testing.T) {
	body := "\n# Acme\nThe api lives in [[deploy]].\n\n- one\n- two\n\n- three\n  more\n\n```go\nfunc main() {\n\n}\n```\n\n    indented\n\n    code\n\nLast line.\n"
	var got []string
	for _, b := range splitBlocks(body) {
		got = append(got, b.Src)
	}
	want := []string{"# Acme\nThe api lives in [[deploy]].", "- one\n- two\n\n- three\n  more", "```go\nfunc main() {\n\n}\n```", "    indented\n\n    code", "Last line."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("blocks = %q", got)
	}
	lines := strings.Split(body, "\n")
	for _, b := range splitBlocks(body) {
		if strings.Join(lines[b.From:b.To], "\n") != b.Src {
			t.Errorf("block %q does not match lines %d-%d", b.Src, b.From, b.To)
		}
	}
}

func TestSpliceBody(t *testing.T) {
	body := "\nFirst.\n\nSecond.\n\nThird.\n"
	for _, c := range []struct {
		from, to int
		text     string
		want     string
	}{
		{3, 4, "Second, edited.\n\nAnd a new one.", "\nFirst.\n\nSecond, edited.\n\nAnd a new one.\n\nThird.\n"},
		{3, 4, "", "\nFirst.\n\nThird.\n"},
		{-1, -1, "Fourth.", "\nFirst.\n\nSecond.\n\nThird.\n\nFourth.\n"},
		{5, 6, "Third.\r\n", body},
	} {
		got, ok := spliceBody(body, c.from, c.to, c.text)
		if !ok || got != c.want {
			t.Errorf("spliceBody(%d, %d, %q) = %q, %v", c.from, c.to, c.text, got, ok)
		}
	}
	if got, _ := spliceBody("\n", -1, -1, "Hello."); got != "\nHello.\n" {
		t.Errorf("appending to an empty body = %q", got)
	}
	if _, ok := spliceBody(body, 4, 99, "x"); ok {
		t.Error("a range past the end was accepted")
	}
}
