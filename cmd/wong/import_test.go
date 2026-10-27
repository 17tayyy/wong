package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	repo := filepath.Join(home, "side-project")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	folder := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(repo)
	mem := filepath.Join(home, ".claude", "projects", folder, "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	note := "---\nname: a\ndescription: d\nmetadata:\n  type: project\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(mem, "a.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	brain := filepath.Join(home, "brain")

	out, err := run(t, "--brain", brain, "import", "--dry-run", "--map", repo+"=sideproject")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{repo + "  -> projects/sideproject  1 note", "dry run: would import 1 note "} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	if out, err := run(t, "--brain", brain, "import"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(brain, "projects", "side-project", "a.md")); err != nil {
		t.Error(err)
	}
	registry, err := os.ReadFile(filepath.Join(home, ".config", "wong", "repos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "side-project:\n  - " + repo + "\n"; string(registry) != want {
		t.Errorf("repos.yaml = %q, want %q", registry, want)
	}
}

func TestImportRejectsBadMap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	if _, err := run(t, "import", "--map", "nothing"); err == nil || !strings.Contains(err.Error(), "want PATH=ID") {
		t.Errorf("error = %v", err)
	}
}
