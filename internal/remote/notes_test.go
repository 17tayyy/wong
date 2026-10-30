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
	p, op, err := store.CreateNote("codex", "acme", n, Checks{})
	if err != nil || p != "projects/acme/deploy-flow.md" || !strings.HasPrefix(op, "create:") {
		t.Fatalf("CreateNote = %q %q %v", p, op, err)
	}
	if got := current(t, store, "projects/acme/MEMORY.md"); got != "- [Deploy flow](deploy-flow.md) — How Acme ships\n" {
		t.Errorf("MEMORY.md = %q", got)
	}
	if _, _, err := store.CreateNote("codex", "acme", n, Checks{}); !errors.Is(err, ErrTaken) {
		t.Errorf("creating it twice: %v", err)
	}
	if _, _, err := store.CreateNote("codex", "../etc", n, Checks{}); !errors.Is(err, ErrInvalid) {
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

	if _, err := store.EditNote("codex", p, "stale", []byte(current(t, store, p)), Checks{}); !errors.Is(err, ErrChanged) {
		t.Errorf("editing over a newer version: %v", err)
	}
	if _, err := store.EditNote("codex", p, Sha([]byte(current(t, store, p))), []byte("no frontmatter"), Checks{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("saving a note that does not parse: %v", err)
	}
	soon := strings.Replace(current(t, store, p), "kind: reference\n", "kind: reference\npending: soon\n", 1)
	if _, err := store.EditNote("codex", p, Sha([]byte(current(t, store, p))), []byte(soon), Checks{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "release") {
		t.Errorf("a pending that is not understood: %v", err)
	}
	if _, err := store.EditNote("codex", p, Sha([]byte(current(t, store, p))), []byte(strings.Replace(soon, "soon", "release", 1)), Checks{}); err != nil {
		t.Errorf("pending: release: %v", err)
	}
	if _, err := store.DeleteNote("codex", "nothing"); !errors.Is(err, ErrNoNote) {
		t.Errorf("deleting a missing note: %v", err)
	}
}

func TestServerNotesHoldNoSecretsAndStayShared(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	n := note.New("Deploy flow", "How Acme ships", note.Reference, "", "\nMerge, then promote.\nThe ci user: aws_access_key_id = "+awsKey+"\n")
	if _, _, err := store.CreateNote("codex", "acme", n, Agent); !errors.Is(err, ErrSecret) ||
		!strings.Contains(err.Error(), "in line 2 of its body (AWS access key ID)") || strings.Contains(err.Error(), awsKey) {
		t.Errorf("a new note with a key in its body: %v", err)
	}
	n = note.New("Deploy flow", "How Acme ships", note.Reference, "", "\nMerge, then promote.\n")
	p, _, err := store.CreateNote("codex", "acme", n, Agent)
	if err != nil {
		t.Fatal(err)
	}
	edit := func(from, to string, checks Checks) error {
		text := current(t, store, p)
		_, err := store.EditNote("codex", p, Sha([]byte(text)), []byte(strings.Replace(text, from, to, 1)), checks)
		return err
	}
	if err := edit("promote.", "promote with "+awsKey+".", Agent); !errors.Is(err, ErrSecret) ||
		!strings.Contains(err.Error(), "at line 11 (AWS access key ID) of the file") || strings.Contains(err.Error(), awsKey) {
		t.Errorf("an edit that adds a key: %v", err)
	}
	for visibility, want := range map[string]string{"local": "saved on the server", "private": `unknown visibility "private"`} {
		if err := edit("kind: reference\n", "kind: reference\nvisibility: "+visibility+"\n", Checks{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("visibility: %s on the server: %v", visibility, err)
		}
	}
	if _, err := store.VerifyNote("codex", p, []string{"linear:ENG-15", "https://console.example.com/?key=" + awsKey}); !errors.Is(err, ErrSecret) ||
		!strings.Contains(err.Error(), "Source 2 holds what looks like a secret (AWS access key ID)") {
		t.Errorf("a source with a key: %v", err)
	}
	if got := current(t, store, p); strings.Contains(got, awsKey) || strings.Contains(got, "visibility") || strings.Contains(got, "verified") {
		t.Errorf("a refused change was saved:\n%s", got)
	}
}

func TestArchiveNote(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := note.New("Old billing", "Billing before Stripe", note.Decision, "", "\nInvoices by hand.\n")
	if _, _, err := store.CreateNote("codex", "acme", old, Checks{}); err != nil {
		t.Fatal(err)
	}

	p, op, err := store.ArchiveNote("codex", "Old billing", false)
	if err != nil || p != "projects/acme/archive/old-billing.md" || !strings.HasPrefix(op, "archive:") {
		t.Fatalf("ArchiveNote = %q %q %v", p, op, err)
	}
	if got := current(t, store, "projects/acme/MEMORY.md"); strings.Contains(got, "old-billing") {
		t.Errorf("the shelf's MEMORY.md still lists it: %q", got)
	}
	if got := current(t, store, "projects/acme/archive/MEMORY.md"); !strings.Contains(got, "(old-billing.md)") {
		t.Errorf("the archive's MEMORY.md = %q", got)
	}
	if _, _, err := store.ArchiveNote("codex", p, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("archiving it twice: %v", err)
	}
	b, _ := store.Brain()
	if got, ok := b.Resolve("Old billing"); !ok || got != p {
		t.Errorf("Resolve of an archived note = %q, %v", got, ok)
	}
	if hits := b.Graph().Search("billing", "acme"); len(hits) != 0 {
		t.Errorf("a plain search found the archived note: %v", hits)
	}
	if _, err := store.UndoOp(op, "browser"); err != nil {
		t.Fatal(err)
	}
	if got := current(t, store, "projects/acme/MEMORY.md"); !strings.Contains(got, "(old-billing.md)") {
		t.Errorf("undoing the archive did not bring its MEMORY.md line back: %q", got)
	}
	if _, _, err := store.ArchiveNote("codex", "old-billing", true); !errors.Is(err, ErrInvalid) {
		t.Errorf("bringing back a note that is not archived: %v", err)
	}
	if _, _, err := store.ArchiveNote("codex", "old-billing", false); err != nil {
		t.Fatal(err)
	}
	now := note.New("Billing", "Billing today", note.Project, "", "\nStripe now; see [[old-billing]].\n")
	if _, _, err := store.CreateNote("codex", "acme", now, Checks{Links: true}); err != nil {
		t.Errorf("a link to an archived note was refused: %v", err)
	}
	if back, _, err := store.ArchiveNote("codex", "old-billing", true); err != nil || back != "projects/acme/old-billing.md" {
		t.Errorf("bring it back = %q, %v", back, err)
	}
}
