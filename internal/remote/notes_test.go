package remote

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/note"
)

func TestNoteOperations(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	n := note.New("Deploy flow", "How Acme ships", note.Reference, "", "\nMerge, then promote.\n")
	n.Sources = []string{"linear:ENG-15"}
	p, op, err := store.CreateNote("codex", "acme", n)
	if err != nil || p != "projects/acme/deploy-flow.md" || !strings.HasPrefix(op, "create:") {
		t.Fatalf("CreateNote = %q %q %v", p, op, err)
	}
	if got := current(t, store, "projects/acme/MEMORY.md"); got != "- [Deploy flow](deploy-flow.md) — How Acme ships\n" {
		t.Errorf("MEMORY.md = %q", got)
	}
	if _, _, err := store.CreateNote("codex", "acme", n); !errors.Is(err, ErrTaken) {
		t.Errorf("creating it twice: %v", err)
	}
	if _, _, err := store.CreateNote("codex", "../etc", n); !errors.Is(err, ErrInvalid) {
		t.Errorf("a shelf outside the brain: %v", err)
	}

	b, err := store.Brain()
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"projects/acme/deploy-flow.md", "projects/acme/deploy-flow", "Deploy flow", "deploy-flow"} {
		if got, ok := b.Resolve(ref); !ok || got != p {
			t.Errorf("Resolve(%q) = %q, %v", ref, got, ok)
		}
	}

	if _, err := store.db.Exec(`INSERT INTO tasks (note, project, severity, ref, message, source, event, created_at) VALUES (?, 'acme', 'stale', 'ENG-15', 'ENG-15 is closed', 'linear', '{}', ?)`,
		p, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyNote("codex", p, nil); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "ENG-15 is closed") {
		t.Errorf("verifying a note with an open task: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET closed_at = ?`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyNote("codex", "deploy-flow", []string{"commit:acme@abc1234"}); err != nil {
		t.Fatal(err)
	}
	if got := current(t, store, p); !strings.Contains(got, "verified: "+time.Now().Format(time.DateOnly)) || !strings.Contains(got, "commit:acme@abc1234") {
		t.Errorf("verified note:\n%s", got)
	}
	if _, err := store.VerifyNote("codex", p, []string{"somewhere"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a source that is not understood: %v", err)
	}

	if _, err := store.EditNote("codex", p, "stale", []byte(current(t, store, p))); !errors.Is(err, ErrChanged) {
		t.Errorf("editing over a newer version: %v", err)
	}
	if _, err := store.EditNote("codex", p, Sha([]byte(current(t, store, p))), []byte("no frontmatter")); !errors.Is(err, ErrInvalid) {
		t.Errorf("saving a note that does not parse: %v", err)
	}
	if _, err := store.DeleteNote("codex", "nothing"); !errors.Is(err, ErrNoNote) {
		t.Errorf("deleting a missing note: %v", err)
	}
}
