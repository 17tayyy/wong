package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/17tayyy/wong/internal/check"
)

func applyAs(t *testing.T, s *Store, machine, path, base, content string) {
	t.Helper()
	up := Upload{Path: path, Base: base}
	if content != "" {
		up.Sha, up.Content = Sha([]byte(content)), []byte(content)
	}
	if _, conflicts, err := s.Apply(machine, []Upload{up}); err != nil || len(conflicts) > 0 {
		t.Fatalf("Apply %s %q: %v, %v", path, content, conflicts, err)
	}
}

func current(t *testing.T, s *Store, path string) string {
	t.Helper()
	files, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return string(f.Content)
		}
	}
	return ""
}

func TestUndoAndRestore(t *testing.T) {
	store, _, _, _ := setup(t)
	applyAs(t, store, "desktop", "me/a.md", "", "one")
	applyAs(t, store, "laptop", "me/a.md", Sha([]byte("one")), "two")
	applyAs(t, store, "laptop", "me/b.md", "", "b")

	log, err := store.Log(10, 0)
	if err != nil || len(log) != 3 || log[0].Path != "me/b.md" || log[1].Machine != "laptop" || log[2].Machine != "desktop" {
		t.Fatalf("Log = %+v, %v", log, err)
	}
	if older, _ := store.Log(10, log[0].Seq); len(older) != 2 {
		t.Errorf("Log before %d = %+v", log[0].Seq, older)
	}

	if err := store.Undo(log[2].Seq, "dashboard"); !errors.Is(err, ErrNotLatest) {
		t.Errorf("undoing a replaced change: %v", err)
	}
	if err := store.Undo(log[1].Seq, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if got := current(t, store, "me/a.md"); got != "one" {
		t.Errorf("after undoing the edit a.md = %q", got)
	}
	if err := store.Undo(log[0].Seq, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if got := current(t, store, "me/b.md"); got != "" {
		t.Errorf("undoing the creation left b.md = %q", got)
	}
	history, err := store.History("me/b.md")
	if err != nil || len(history) != 2 || !history[0].Deleted || history[0].Machine != "dashboard" {
		t.Fatalf("History(b) = %+v, %v", history, err)
	}
	if err := store.Undo(history[0].Seq, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if got := current(t, store, "me/b.md"); got != "b" {
		t.Errorf("undoing the deletion gave b.md = %q", got)
	}

	if err := store.Restore(log[1].Seq, Sha([]byte("one")), "dashboard"); err != nil {
		t.Fatal(err)
	}
	if got := current(t, store, "me/a.md"); got != "two" {
		t.Errorf("restored a.md = %q", got)
	}
	if err := store.Restore(log[2].Seq, Sha([]byte("stale base")), "dashboard"); !errors.Is(err, ErrNotLatest) {
		t.Errorf("restoring over a newer version: %v", err)
	}
}

func TestFindingsAreReportedPerMachine(t *testing.T) {
	store, _, desktop, laptop := setup(t)
	stale := check.Finding{Note: "projects/acme/a.md", Severity: check.Stale, Ref: "ENG-15", Message: "Done since 2026-10-20"}
	review := check.Finding{Note: "projects/acme/b.md", Severity: check.Review, Ref: "acme-api/x.go", Message: "not found"}
	ctx := context.Background()

	if err := desktop.client.ReportFindings(ctx, "acme", []check.Finding{stale, review}); err != nil {
		t.Fatal(err)
	}
	if err := laptop.client.ReportFindings(ctx, "acme", []check.Finding{stale}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Findings("acme"); err != nil || len(got) != 2 || got[0] != stale || got[1] != review {
		t.Fatalf("Findings = %+v, %v", got, err)
	}
	if err := desktop.client.ReportFindings(ctx, "acme", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Findings("acme"); len(got) != 1 || got[0] != stale {
		t.Errorf("after the desktop's empty report = %+v", got)
	}
	applyAs(t, store, "laptop", "projects/acme/a.md", "", "fixed")
	if got, _ := store.Findings(""); len(got) != 0 {
		t.Errorf("a push of the note kept its findings: %+v", got)
	}
}
