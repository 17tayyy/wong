package link

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "acme-api")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return repo
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func ignored(t *testing.T, repo string) bool {
	t.Helper()
	return exec.Command("git", "-C", repo, "check-ignore", "-q", settingsPath).Run() == nil
}

func TestMemoryDir(t *testing.T) {
	tests := []struct{ brain, home, want string }{
		{"/home/dev/.wong", "/home/dev", "~/.wong/projects/acme"},
		{"/srv/brain", "/home/dev", "/srv/brain/projects/acme"},
	}
	for _, tt := range tests {
		if got := MemoryDir(tt.brain, tt.home, "acme"); got != tt.want {
			t.Errorf("MemoryDir(%q, %q) = %q, want %q", tt.brain, tt.home, got, tt.want)
		}
	}
}

func TestLinkCreatesSettingsAndIgnoresThem(t *testing.T) {
	repo := newRepo(t)

	if err := Link(repo, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, filepath.Join(repo, settingsPath)); got != "{\n  \"autoMemoryDirectory\": \"~/.wong/projects/acme\"\n}\n" {
		t.Errorf("settings = %q", got)
	}
	if !ignored(t, repo) {
		t.Error("settings file is not ignored by git")
	}
	if dir, err := Current(repo); err != nil || dir != "~/.wong/projects/acme" {
		t.Errorf("Current() = %q, %v", dir, err)
	}
}

func TestLinkKeepsOtherSettingsAndAddsTheIgnoreOnce(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, settingsPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions": {"allow": ["Bash(go test:*)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := Link(repo, "~/.wong/projects/acme"); err != nil {
			t.Fatal(err)
		}
	}

	got := readFile(t, path)
	if !strings.Contains(got, `"Bash(go test:*)"`) || !strings.Contains(got, `"autoMemoryDirectory": "~/.wong/projects/acme"`) {
		t.Errorf("settings = %s", got)
	}
	if n := strings.Count(readFile(t, filepath.Join(repo, ".git", "info", "exclude")), settingsPath); n != 1 {
		t.Errorf("exclude lists the settings file %d times", n)
	}
}

func TestLinkLeavesAnExistingIgnoreAlone(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".claude/settings.local.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Link(repo, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, filepath.Join(repo, ".git", "info", "exclude")); strings.Contains(got, settingsPath) {
		t.Errorf("exclude = %q", got)
	}
}

func TestUnlink(t *testing.T) {
	repo := newRepo(t)
	path := filepath.Join(repo, settingsPath)

	if err := Link(repo, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}
	if err := Unlink(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("settings file left behind: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"model": "opus"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Link(repo, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}
	if err := Unlink(repo); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "{\n  \"model\": \"opus\"\n}\n" {
		t.Errorf("settings = %q", got)
	}
	if err := Unlink(repo); err != nil {
		t.Errorf("Unlink() of an unlinked repo: %v", err)
	}
}
