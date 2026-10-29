package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/remote"
)

// machine gives a fresh home with a repo and its Claude Code memory, and makes it the current one.
func machine(t *testing.T, root, name, file string) (home, repo string) {
	t.Helper()
	home = filepath.Join(root, name)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	repo = filepath.Join(home, "acme", "acme-api")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	folder := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(repo)
	mem := filepath.Join(home, ".claude", "projects", folder, "memory")
	stem := strings.TrimSuffix(file, ".md")
	for path, content := range map[string]string{
		file:        "---\nname: " + stem + "\ndescription: from the " + name + "\nmetadata:\n  type: project\n---\nBody.\n",
		"MEMORY.md": "- [" + stem + "](" + file + ") — from the " + name + "\n",
	} {
		if err := os.MkdirAll(mem, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mem, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, repo
}

// fakeClaude puts on PATH only git and a claude that records the commands it gets, and returns that record.
func fakeClaude(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(bin, "claude.log")
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"$*\" >> "+record+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return record
}

func TestSetupBringsEachMachinesMemoryToTheServer(t *testing.T) {
	record := fakeClaude(t)
	root := t.TempDir()
	store, err := remote.Open(filepath.Join(root, "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := httptest.NewServer(remote.Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), remote.Webhooks{}))
	defer srv.Close()

	desktop, repo := machine(t, root, "desktop", "deploy.md")
	token, err := store.NewToken("desktop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runWithInput(t, "wrong\n", "setup", srv.URL); err == nil || !strings.Contains(err.Error(), "check the url and the token") {
		t.Errorf("setup with a wrong token: %v", err)
	}
	out, err := runWithInput(t, token+"\n", "setup", srv.URL)
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	for _, want := range []string{"pulled: 0 updated", "imported 1 note", "pushed: 2", repo + "  -> ", "Installed the wong plugin"} {
		if !strings.Contains(out, want) {
			t.Errorf("desktop setup lacks %q:\n%s", want, out)
		}
	}
	if data, _ := os.ReadFile(record); string(data) != "plugin marketplace add 17tayyy/wong\nplugin install wong@wong\n" {
		t.Errorf("claude was run with %q", data)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "settings.local.json")); err != nil {
		t.Errorf("the repo was not linked: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(desktop, ".claude", "settings.json")); err != nil || !strings.Contains(string(data), "autoMemoryDirectory") {
		t.Errorf("me/ is not the default memory: %s, %v", data, err)
	}

	laptop, _ := machine(t, root, "laptop", "oncall.md")
	if token, err = store.NewToken("laptop"); err != nil {
		t.Fatal(err)
	}
	if out, err := runWithInput(t, token+"\n", "setup", srv.URL); err != nil || !strings.Contains(out, "pulled: 2 updated") {
		t.Fatalf("laptop setup: %v\n%s", err, out)
	}
	index, err := os.ReadFile(filepath.Join(laptop, ".wong", "projects", "acme", "MEMORY.md"))
	if err != nil || !strings.Contains(string(index), "(deploy.md)") || !strings.Contains(string(index), "(oncall.md)") {
		t.Errorf("laptop index = %s, %v", index, err)
	}

	machine(t, root, "desktop", "deploy.md")
	if out, err := run(t, "pull"); err != nil || !strings.Contains(out, "pulled: 2 updated") {
		t.Errorf("desktop pull: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(desktop, ".wong", "projects", "acme", "oncall.md")); err != nil {
		t.Errorf("the laptop's note did not reach the desktop: %v", err)
	}
}

func TestSetupWithoutAServer(t *testing.T) {
	record := fakeClaude(t)
	os.Remove(filepath.Join(filepath.Dir(record), "claude"))
	home, repo := machine(t, t.TempDir(), "laptop", "deploy.md")
	out, err := run(t, "setup")
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if strings.Contains(out, "pulled") || strings.Contains(out, "pushed") || !strings.Contains(out, "imported 1 note") ||
		!strings.Contains(out, repo+"  -> ") || !strings.Contains(out, "Claude Code is not on this machine's PATH") {
		t.Errorf("setup without a server:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "wong", "remote.yaml")); err == nil {
		t.Error("setup without a server saved one")
	}
}
