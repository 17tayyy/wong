package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func noteEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("LINEAR_API_KEY", "")
	brain := filepath.Join(home, ".wong")
	for rel, content := range map[string]string{
		"projects/acme/broken.md": "---\nname: broken\ndescription: d\nkind: project\nscope: project:acme\n---\nSee [[gone]].\n",
		"projects/acme/orca.md":   "---\nname: orca\ndescription: the orca cli\nkind: reference\nscope: project:acme\n---\nUse orca.exe.\n",
		"projects/acme/MEMORY.md": "- [Broken](broken.md) · [Orca](orca.md) — two notes\n",
	} {
		path := filepath.Join(brain, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return brain
}

func runWithInput(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestNoteShow(t *testing.T) {
	brain := noteEnv(t)

	out, err := run(t, "--brain", brain, "note", "show", "broken")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"~/.wong/projects/acme/broken.md\n", "See [[gone]].", "findings:\n  stale  [[gone]]  links to a note that does not exist"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestNoteVerifyRefusesAndRestores(t *testing.T) {
	brain := noteEnv(t)
	path := filepath.Join(brain, "projects/acme/broken.md")
	before, _ := os.ReadFile(path)

	out, err := run(t, "--brain", brain, "note", "verify", "broken", "--source", "note:orca")
	if err == nil || !strings.Contains(err.Error(), "still has 1 findings") || !strings.Contains(out, "[[gone]]") {
		t.Errorf("verify of a broken note: %v\n%s", err, out)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Errorf("note changed after a refused verify:\n%s", after)
	}

	if _, err := run(t, "--brain", brain, "note", "verify", "broken", "--source", "jira:ENG-1"); err == nil {
		t.Error("an unknown source was accepted")
	}
	if _, err := run(t, "--brain", brain, "note", "verify", "orca", "--source", "linear:ENG-1"); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Errorf("a Linear source without a key: %v", err)
	}
}

func TestNoteVerify(t *testing.T) {
	brain := noteEnv(t)
	path := filepath.Join(brain, "projects/acme/broken.md")
	if err := os.WriteFile(path, []byte("---\nname: broken\ndescription: d\nkind: project\nscope: project:acme\n---\nSee [[orca]].\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "--brain", brain, "note", "verify", "projects/acme/broken.md", "--source", "note:orca")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(path)
	today := time.Now().Format(time.DateOnly)
	if !strings.Contains(string(got), "sources:\n  - note:orca\nverified: "+today+"\n") {
		t.Errorf("note = %s", got)
	}
}

func TestNoteWriteAndMove(t *testing.T) {
	brain := noteEnv(t)

	out, err := runWithInput(t, "Billing moves to Stripe; see ENG-15.\n", "--brain", brain, "note", "write",
		"--to", "acme", "--name", "move-billing", "--description", "Decided to move billing to Stripe",
		"--kind", "decision", "--source", "note:orca")
	if err != nil || !strings.Contains(out, "wrote projects/acme/move-billing.md") {
		t.Fatalf("write: %v\n%s", err, out)
	}
	index, _ := os.ReadFile(filepath.Join(brain, "projects/acme/MEMORY.md"))
	if !strings.Contains(string(index), "- [move-billing](move-billing.md) — Decided to move billing to Stripe\n") {
		t.Errorf("index = %s", index)
	}
	if _, err := runWithInput(t, "", "--brain", brain, "note", "write", "--to", "acme", "--name", "x", "--description", "d", "--kind", "project"); err == nil {
		t.Error("a note without a body was written")
	}
	if _, err := runWithInput(t, "b", "--brain", brain, "note", "write", "--to", "acme", "--name", "x", "--description", "d", "--kind", "feedback"); err == nil {
		t.Error("an unknown kind was accepted")
	}

	out, err = run(t, "--brain", brain, "note", "move", "orca", "--to", "me")
	if err != nil || !strings.Contains(out, "moved projects/acme/orca.md -> me/orca.md") {
		t.Fatalf("move: %v\n%s", err, out)
	}
	index, _ = os.ReadFile(filepath.Join(brain, "projects/acme/MEMORY.md"))
	if strings.Contains(string(index), "orca.md") || !strings.Contains(string(index), "[Broken](broken.md) — two notes") {
		t.Errorf("index after move = %s", index)
	}
	if out, err := run(t, "--brain", brain, "note", "show", "move-billing"); err != nil || strings.Contains(out, "findings:") {
		t.Errorf("the moved note broke a link: %v\n%s", err, out)
	}
}
