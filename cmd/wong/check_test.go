package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("LINEAR_API_KEY", "")
	brain := filepath.Join(home, "brain")
	for rel, body := range map[string]string{"projects/acme/a.md": "See [[gone]].\n", "me/b.md": "Fine.\n"} {
		path := filepath.Join(brain, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		note := "---\nname: " + strings.TrimSuffix(filepath.Base(rel), ".md") + "\ndescription: d\n---\n" + body
		if err := os.WriteFile(path, []byte(note), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := run(t, "--brain", brain, "check")
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	for _, want := range []string{
		"projects/acme/a.md\n  stale  [[gone]]  links to a note that does not exist\n",
		"checked 2 notes: 1 stale, 0 to review, 1 with no findings\n",
		"skipped: projects/acme: no local repos registered",
		"skipped: Linear issues: set LINEAR_API_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
