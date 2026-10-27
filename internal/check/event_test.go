package check

import (
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/linear"
)

func eventNote(body string, written string) Note {
	return Note{
		Path:    "projects/acme/a.md",
		Data:    []byte("---\nname: a\ndescription: d\n---\n" + body),
		Written: date(written),
	}
}

func TestIssueClosed(t *testing.T) {
	closed := linear.Issue{Identifier: "ENG-1", State: "Done", Type: "completed", ClosedAt: date("2026-10-20")}
	tests := []struct {
		name, body, written string
		want                Severity
	}{
		{"open word", "ENG-1 en pausa.\n", "2026-10-25", Stale},
		{"closed after the note", "Tracked in ENG-1.\n", "2026-10-10", Review},
		{"closed before the note", "Tracked in ENG-1.\n", "2026-10-25", ""},
		{"not cited", "Tracked in ENG-2.\n", "2026-10-10", ""},
	}
	for _, tt := range tests {
		f, found, err := IssueClosed(eventNote(tt.body, tt.written), closed)
		if err != nil {
			t.Fatal(err)
		}
		if found != (tt.want != "") || found && (f.Severity != tt.want || f.Ref != "ENG-1" || f.Note != "projects/acme/a.md") {
			t.Errorf("%s: %+v, %v", tt.name, f, found)
		}
	}

	pref := Note{Path: "me/p.md", Data: []byte("---\nname: p\ndescription: d\nkind: preference\n---\nENG-1 en pausa.\n"), Written: date("2026-10-01")}
	if _, found, _ := IssueClosed(pref, closed); found {
		t.Error("a preference was checked against an issue state")
	}
}

func TestFileRemoved(t *testing.T) {
	when := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, body, written string
		found               bool
	}{
		{"exact path", "Lives in `app/icons.ts`.\n", "2026-09-01", true},
		{"with the repo name", "Lives in `acme-web/app/icons.ts`.\n", "2026-09-01", true},
		{"bare file name", "Lives in `icons.ts`.\n", "2026-09-01", true},
		{"note written after", "Removed `app/icons.ts`.\n", "2026-10-20", false},
		{"other file", "Lives in `app/other.ts`.\n", "2026-09-01", false},
	}
	for _, tt := range tests {
		f, found, err := FileRemoved(eventNote(tt.body, tt.written), "acme-web", "src/app/icons.ts", "cdbec4d", when)
		if err != nil {
			t.Fatal(err)
		}
		if found != tt.found || found && (f.Severity != Stale || f.Message != "deleted or moved in cdbec4d on 2026-10-13, after the note was last updated on 2026-09-01") {
			t.Errorf("%s: %+v, %v", tt.name, f, found)
		}
	}
}
