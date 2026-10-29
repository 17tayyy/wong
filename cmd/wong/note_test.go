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

func TestNoteRemoveAndTidy(t *testing.T) {
	brain := noteEnv(t)

	out, err := run(t, "--brain", brain, "tidy")
	if err != nil || !strings.Contains(out, "Preferences that may say the same thing (0 groups)") || !strings.Contains(out, "Notes that may be in the wrong folder (0)") {
		t.Errorf("tidy: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(brain, "projects/acme/broken.md"), []byte("---\nname: broken\ndescription: d\n---\nSee [[orca]].\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "--brain", brain, "note", "remove", "orca")
	if err != nil || !strings.Contains(out, "removed projects/acme/orca.md") || !strings.Contains(out, "projects/acme/broken.md links to it") {
		t.Errorf("remove: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(brain, "projects/acme/orca.md")); !os.IsNotExist(err) {
		t.Errorf("note still there: %v", err)
	}
}

func TestNoteWriteKeepsTagsLinksAndNotesTidy(t *testing.T) {
	brain := noteEnv(t)
	write := func(body string, args ...string) (string, error) {
		return runWithInput(t, body, append([]string{"--brain", brain, "note", "write", "--to", "acme", "--kind", "reference"}, args...)...)
	}

	if out, err := write("Builds images. Tracked in ENG-15.\n", "--name", "ci-pipeline", "--description", "How CI builds images", "--tag", "#Deploy", "--tag", "ci"); err != nil {
		t.Fatalf("write with tags: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(brain, "projects/acme/ci-pipeline.md"))
	if !strings.Contains(string(data), "tags: [deploy, ci]") {
		t.Errorf("the tags were not normalized:\n%s", data)
	}
	if _, err := write("x\n", "--name", "previews", "--description", "One preview per pull request", "--tag", "deployment"); err == nil || !strings.Contains(err.Error(), `use "deploy"`) {
		t.Errorf("a spelling of a tag in use: %v", err)
	}
	if _, err := write("Built as in [[ci-pipelin]].\n", "--name", "previews", "--description", "One preview per pull request"); err == nil || !strings.Contains(err.Error(), "did you mean [[ci-pipeline]]") {
		t.Errorf("a link to no note: %v", err)
	}
	if _, err := write("Build images.\n", "--name", "image-builds", "--description", "How CI builds images"); err == nil || !strings.Contains(err.Error(), "projects/acme/ci-pipeline.md") {
		t.Errorf("a note on a subject already covered: %v", err)
	}
	out, err := write("Tagged with the commit. Tracked in ENG-15.\n", "--name", "image-tags", "--description", "Images carry the commit as their tag", "--tag", "deploy")
	if err != nil || !strings.Contains(out, "[[ci-pipeline]] (projects/acme/ci-pipeline.md, shares ENG-15, #deploy)") {
		t.Errorf("related notes after writing: %v\n%s", err, out)
	}
	if out, err := run(t, "--brain", brain, "tags"); err != nil || out != "deploy (2)\nci (1)\n" {
		t.Errorf("wong tags = %q, %v", out, err)
	}
}

func TestNoteSearch(t *testing.T) {
	brain := noteEnv(t)
	out, err := run(t, "--brain", brain, "note", "search", "ORCA")
	if err != nil || !strings.Contains(out, "1 notes match:") || !strings.Contains(out, "projects/acme/orca.md — the orca cli") || !strings.Contains(out, "> Use orca.exe.") {
		t.Errorf("search: %v\n%s", err, out)
	}
	if out, _ := run(t, "--brain", brain, "note", "search", "nowhere"); !strings.Contains(out, "no note holds all of: nowhere") {
		t.Errorf("search with no match:\n%s", out)
	}
}

func TestNoteArchive(t *testing.T) {
	brain := noteEnv(t)
	if out, err := run(t, "--brain", brain, "note", "archive", "orca"); err != nil || !strings.Contains(out, "archived projects/acme/orca.md -> projects/acme/archive/orca.md") {
		t.Fatalf("archive: %v\n%s", err, out)
	}
	if out, _ := run(t, "--brain", brain, "note", "search", "orca"); !strings.Contains(out, "no note holds all of: orca") {
		t.Errorf("a plain search found an archived note:\n%s", out)
	}
	if out, err := run(t, "--brain", brain, "note", "search", "--archive", "orca"); err != nil || !strings.Contains(out, "1 archived notes match:") || !strings.Contains(out, "projects/acme/archive/orca.md") {
		t.Errorf("search --archive: %v\n%s", err, out)
	}
	if out, err := run(t, "--brain", brain, "note", "show", "orca"); err != nil || !strings.Contains(out, "archived: wong check leaves it alone") {
		t.Errorf("show an archived note: %v\n%s", err, out)
	}
	if out, err := run(t, "--brain", brain, "note", "unarchive", "orca"); err != nil || !strings.Contains(out, "brought back projects/acme/archive/orca.md -> projects/acme/orca.md") {
		t.Errorf("unarchive: %v\n%s", err, out)
	}
}
