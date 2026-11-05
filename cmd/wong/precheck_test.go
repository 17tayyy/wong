package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrecheckWarnsOfPitfallsInTheCommit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	repo := filepath.Join(home, "acme-api")
	for _, args := range [][]string{{"init", "-q", repo}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	files := map[string]string{
		filepath.Join(home, ".config/wong/repos.yaml"):        "acme:\n  - " + repo + "\n",
		filepath.Join(home, ".wong/projects/acme/fragile.md"): "---\nname: fragile-billing\ndescription: retries double-charge\nkind: pitfall\nscope: project:acme\n---\nRetrying in `app/billing.py` charged twice.\n",
		filepath.Join(home, ".wong/projects/acme/billing.md"): "---\nname: billing\ndescription: how billing works\nkind: reference\nscope: project:acme\n---\nSee `app/billing.py`.\n",
		filepath.Join(repo, "app/billing.py"):                 "pass\n",
		filepath.Join(repo, "app/other.py"):                   "pass\n",
		filepath.Join(home, ".wong/projects/acme/MEMORY.md"):  "",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)
	stage := func(name string) {
		t.Helper()
		if out, err := exec.Command("git", "add", name).CombinedOutput(); err != nil {
			t.Fatalf("git add: %v\n%s", err, out)
		}
	}

	stage("app/other.py")
	if out := mustRun(t, "precheck"); out != "" {
		t.Errorf("a commit with no pitfall: %q", out)
	}
	stage("app/billing.py")
	out := mustRun(t, "precheck")
	if !strings.Contains(out, "app/billing.py:\n- fragile-billing (~/.wong/projects/acme/fragile.md), a pitfall: retries double-charge\n") ||
		strings.Contains(out, "- billing (") || strings.Contains(out, "other.py") {
		t.Errorf("precheck:\n%s", out)
	}

	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if out := mustRun(t, "precheck", "--install"); !strings.Contains(out, "Added wong precheck") {
		t.Errorf("install: %s", out)
	}
	if data, _ := os.ReadFile(hook); string(data) != "#!/bin/sh\nwong precheck || true\n" {
		t.Errorf("pre-commit = %q", data)
	}
	if out := mustRun(t, "precheck", "--install"); !strings.Contains(out, "already runs wong precheck") {
		t.Errorf("a second install: %s", out)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nmake lint\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, "precheck", "--install"); !strings.Contains(out, "add this line to it") {
		t.Errorf("install over another hook: %s", out)
	}
	if data, _ := os.ReadFile(hook); string(data) != "#!/bin/sh\nmake lint\n" {
		t.Errorf("another hook changed: %q", data)
	}
}
