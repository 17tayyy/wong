package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	repo := filepath.Join(home, "acme", "acme-api")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	brain := filepath.Join(home, ".wong")
	settings := filepath.Join(repo, ".claude", "settings.local.json")
	t.Chdir(repo)

	if _, err := run(t, "--brain", brain, "link"); err == nil || !strings.Contains(err.Error(), "pass --project ID") {
		t.Errorf("link without a known project: %v", err)
	}

	out, err := run(t, "--brain", brain, "link", "--project", "acme")
	if err != nil {
		t.Fatalf("link: %v\n%s", err, out)
	}
	if !strings.Contains(out, repo+"  -> ~/.wong/projects/acme") {
		t.Errorf("output = %q", out)
	}
	data, err := os.ReadFile(settings)
	if err != nil || !strings.Contains(string(data), `"autoMemoryDirectory": "~/.wong/projects/acme"`) {
		t.Errorf("settings = %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(brain, "projects", "acme")); err != nil {
		t.Errorf("project folder: %v", err)
	}

	if out, err := run(t, "--brain", brain, "link", "--all", "--remove"); err != nil || !strings.Contains(out, repo+"  unlinked") {
		t.Fatalf("unlink all: %v\n%s", err, out)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("settings left behind: %v", err)
	}

	if out, err := run(t, "--brain", brain, "link"); err != nil {
		t.Errorf("relink from the registry: %v\n%s", err, out)
	}
}
