package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/note"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func noteText(name, scope string) string {
	return "---\nname: " + name + "\ndescription: about " + name + "\nmetadata:\n  type: project\nkind: project\nscope: " + scope + "\n---\nBody.\n"
}

func newBrain(t *testing.T) string {
	t.Helper()
	brain := t.TempDir()
	write(t, filepath.Join(brain, "me", "use-uv.md"), noteText("use-uv", "global"))
	write(t, filepath.Join(brain, "me", "MEMORY.md"), "- [Use uv](use-uv.md) — uv run\n")
	write(t, filepath.Join(brain, "projects", "acme", "a.md"), noteText("billing-migration", "project:acme"))
	write(t, filepath.Join(brain, "projects", "acme", "orca.md"), noteText("orca", "project:acme"))
	write(t, filepath.Join(brain, "projects", "acme", "MEMORY.md"),
		"# Memory Index\n\n## Work\n- [A](a.md) · [Orca](orca.md) — two notes\n- [Orca alone](orca.md) — again\n")
	write(t, filepath.Join(brain, "projects", "other", "a.md"), noteText("other-a", "project:other"))
	return brain
}

func TestResolve(t *testing.T) {
	brain := newBrain(t)
	tests := map[string]string{
		"projects/acme/a.md": "projects/acme/a.md",
		filepath.Join(brain, "projects", "acme", "orca.md"): "projects/acme/orca.md",
		"billing-migration": "projects/acme/a.md",
		"use-uv":            "me/use-uv.md",
	}
	for ref, want := range tests {
		if got, err := Resolve(brain, ref); err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	for ref, wantErr := range map[string]string{
		"a":                  "matches several notes",
		"nothing":            "no note named",
		"projects/acme/z.md": "no note projects/acme/z.md",
		"/elsewhere/x.md":    "is not in the brain",
	} {
		if _, err := Resolve(brain, ref); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Resolve(%q) error = %v, want %q", ref, err, wantErr)
		}
	}
}

func TestMoveUpdatesScopeAndBothIndexes(t *testing.T) {
	brain := newBrain(t)

	dest, err := Move(brain, "projects/acme/orca.md", "me")
	if err != nil {
		t.Fatal(err)
	}

	if dest != "me/orca.md" {
		t.Errorf("dest = %q", dest)
	}
	if _, err := os.Stat(filepath.Join(brain, "projects", "acme", "orca.md")); !os.IsNotExist(err) {
		t.Errorf("source still there: %v", err)
	}
	if got := read(t, filepath.Join(brain, "me", "orca.md")); !strings.Contains(got, "scope: global\n") {
		t.Errorf("moved note = %s", got)
	}
	if got, want := read(t, filepath.Join(brain, "projects", "acme", "MEMORY.md")), "# Memory Index\n\n## Work\n- [A](a.md) — two notes\n"; got != want {
		t.Errorf("acme index = %q, want %q", got, want)
	}
	if got, want := read(t, filepath.Join(brain, "me", "MEMORY.md")), "- [Use uv](use-uv.md) — uv run\n- [orca](orca.md) — about orca\n"; got != want {
		t.Errorf("me index = %q, want %q", got, want)
	}
}

func TestMoveRefusesCollisionsAndBadTargets(t *testing.T) {
	brain := newBrain(t)

	if _, err := Move(brain, "projects/acme/a.md", "other"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("collision: %v", err)
	}
	if _, err := Move(brain, "projects/acme/a.md", "acme"); err == nil || !strings.Contains(err.Error(), "already in") {
		t.Errorf("same folder: %v", err)
	}
	if _, err := Move(brain, "projects/acme/a.md", "Not Valid"); err == nil {
		t.Error("an invalid project id was accepted")
	}
}

func TestWrite(t *testing.T) {
	brain := newBrain(t)
	n := note.New("move-billing", "Decided to move billing to Stripe", note.Decision, "project:acme", "\nBody.\n")

	rel, err := Write(brain, "projects/acme", n)
	if err != nil {
		t.Fatal(err)
	}

	if rel != "projects/acme/move-billing.md" {
		t.Errorf("rel = %q", rel)
	}
	if got := read(t, filepath.Join(brain, rel)); !strings.Contains(got, "kind: decision\n") || !strings.Contains(got, "  type: project\n") {
		t.Errorf("note = %s", got)
	}
	if got := read(t, filepath.Join(brain, "projects", "acme", "MEMORY.md")); !strings.HasSuffix(got, "- [move-billing](move-billing.md) — Decided to move billing to Stripe\n") {
		t.Errorf("index = %q", got)
	}
	if _, err := Write(brain, "projects/acme", n); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second write: %v", err)
	}

	spaced := note.New("Billing: why Stripe", "d", note.Decision, "project:acme", "\nBody.\n")
	if rel, err := Write(brain, "projects/acme", spaced); err != nil || rel != "projects/acme/billing-why-stripe.md" {
		t.Errorf("Write() with a free-form name = %q, %v", rel, err)
	}
}

func TestRemove(t *testing.T) {
	brain := newBrain(t)
	write(t, filepath.Join(brain, "projects", "other", "b.md"), noteText("b", "project:other")+"See [[orca]].\n")

	linkers, err := Remove(brain, "projects/acme/orca.md")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(brain, "projects", "acme", "orca.md")); !os.IsNotExist(err) {
		t.Errorf("note still there: %v", err)
	}
	if got := read(t, filepath.Join(brain, "projects", "acme", "MEMORY.md")); strings.Contains(got, "orca.md") {
		t.Errorf("index = %q", got)
	}
	if len(linkers) != 1 || linkers[0] != "projects/other/b.md" {
		t.Errorf("linkers = %v", linkers)
	}
}
