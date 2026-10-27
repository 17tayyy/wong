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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ignored(t *testing.T, repo string) bool {
	t.Helper()
	return exec.Command("git", "-C", repo, "check-ignore", "-q", settingsPath).Run() == nil
}

func TestMemoryDir(t *testing.T) {
	tests := []struct{ brain, home, rel, want string }{
		{"/home/dev/.wong", "/home/dev", "projects/acme", "~/.wong/projects/acme"},
		{"/home/dev/.wong", "/home/dev", "me", "~/.wong/me"},
		{"/srv/brain", "/home/dev", "projects/acme", "/srv/brain/projects/acme"},
	}
	for _, tt := range tests {
		if got := MemoryDir(tt.brain, tt.home, tt.rel); got != tt.want {
			t.Errorf("MemoryDir(%q, %q, %q) = %q, want %q", tt.brain, tt.home, tt.rel, got, tt.want)
		}
	}
}

func TestLinkCreatesSettingsAndIgnoresThem(t *testing.T) {
	repo := newRepo(t)

	if err := Link(repo, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, Settings(repo)); got != "{\n  \"autoMemoryDirectory\": \"~/.wong/projects/acme\"\n}\n" {
		t.Errorf("settings = %q", got)
	}
	if !ignored(t, repo) {
		t.Error("settings file is not ignored by git")
	}
	if dir, err := Current(Settings(repo)); err != nil || dir != "~/.wong/projects/acme" {
		t.Errorf("Current() = %q, %v", dir, err)
	}
}

func TestLinkThenUnlinkLeavesOtherSettingsByteForByte(t *testing.T) {
	originals := []string{
		`{"model": "opus"}`,
		"{\n  \"permissions\": {\n    \"allow\": [\"Bash(go test:*)\"]\n  }\n}\n",
		"{\n  \"model\": \"opus\",\n  \"autoMemoryDirectory\": \"~/old\"\n}\n",
	}
	wants := []string{
		`{"model": "opus"}`,
		"{\n  \"permissions\": {\n    \"allow\": [\"Bash(go test:*)\"]\n  }\n}\n",
		"{\n  \"model\": \"opus\"\n}\n",
	}
	for i, original := range originals {
		repo := newRepo(t)
		writeFile(t, Settings(repo), original)

		for range 2 {
			if err := Link(repo, "~/.wong/projects/acme"); err != nil {
				t.Fatal(err)
			}
		}
		linked := readFile(t, Settings(repo))
		if strings.Count(linked, "autoMemoryDirectory") != 1 || !strings.Contains(linked, `"autoMemoryDirectory": "~/.wong/projects/acme"`) {
			t.Errorf("linked settings = %s", linked)
		}
		if err := Unlink(repo); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, Settings(repo)); got != wants[i] {
			t.Errorf("after unlink = %q, want %q", got, wants[i])
		}
	}
}

func TestUnlinkRemovesAFileLeftEmpty(t *testing.T) {
	repo := newRepo(t)
	if err := Link(repo, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}
	if err := Unlink(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Settings(repo)); !os.IsNotExist(err) {
		t.Errorf("settings file left behind: %v", err)
	}
	if err := Unlink(repo); err != nil {
		t.Errorf("Unlink() of an unlinked repo: %v", err)
	}
}

func TestUnsetKeepsAnEmptyGlobalFile(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := Set(settings, "~/.wong/me"); err != nil {
		t.Fatal(err)
	}
	if err := Unset(settings, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, settings); got != "{\n}\n" {
		t.Errorf("settings = %q", got)
	}
}

func TestLinkAddsTheIgnoreOnlyWhenNeeded(t *testing.T) {
	repo := newRepo(t)
	for range 2 {
		if err := Link(repo, "~/.wong/projects/acme"); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(readFile(t, filepath.Join(repo, ".git", "info", "exclude")), settingsPath); n != 1 {
		t.Errorf("exclude lists the settings file %d times", n)
	}

	ignoredRepo := newRepo(t)
	writeFile(t, filepath.Join(ignoredRepo, ".gitignore"), ".claude/settings.local.json\n")
	if err := Link(ignoredRepo, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(ignoredRepo, ".git", "info", "exclude")); strings.Contains(got, settingsPath) {
		t.Errorf("exclude = %q", got)
	}
}

func TestLinkAFolderThatIsNotARepo(t *testing.T) {
	dir := t.TempDir()
	if err := Link(dir, "~/.wong/projects/acme"); err != nil {
		t.Fatal(err)
	}
	if got, err := Current(Settings(dir)); err != nil || got != "~/.wong/projects/acme" {
		t.Errorf("Current() = %q, %v", got, err)
	}
}
