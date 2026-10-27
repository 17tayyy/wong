package repos

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitHubRepo(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/acme/acme-api.git\n":        "acme/acme-api",
		"https://github.com/acme/acme-api":              "acme/acme-api",
		"https://token@github.com/acme/acme-api/":       "acme/acme-api",
		"git@github.com:acme/acme.web.git":              "acme/acme.web",
		"ssh://git@github.com:22/acme/acme-api.git":     "acme/acme-api",
		"https://gitlab.com/acme/acme-api.git":          "",
		"https://github.com.evil.example/acme/acme-api": "",
		"/srv/git/acme-api.git":                         "",
	} {
		got, ok := GitHubRepo(url)
		if got != want || ok != (want != "") {
			t.Errorf("GitHubRepo(%q) = %q, %v; want %q", url, got, ok, want)
		}
	}
}

func gitRepo(t *testing.T, dir, origin string) {
	t.Helper()
	args := [][]string{{"init", "-q", dir}}
	if origin != "" {
		args = append(args, []string{"-C", dir, "remote", "add", "origin", origin})
	}
	for _, a := range args {
		if out, err := exec.Command("git", a...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
}

func TestWriteRemotes(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	home := t.TempDir()
	brain := filepath.Join(home, ".wong")
	api, web, local, plain := filepath.Join(home, "acme-api"), filepath.Join(home, "web"), filepath.Join(home, "notes"), filepath.Join(home, "plain")
	gitRepo(t, api, "git@github.com:acme/acme-api.git")
	gitRepo(t, web, "https://github.com/acme/acme-web")
	gitRepo(t, local, "")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(brain, 0o755); err != nil {
		t.Fatal(err)
	}
	other := "acme/acme-mobile:\n  project: acme\n  dir: acme-mobile\nacme/acme-web:\n  project: old\n  dir: acme-web\n"
	if err := os.WriteFile(filepath.Join(brain, RemotesFile), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteRemotes(brain, Registry{"acme": {api, web}, "side": {local, plain}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(brain, RemotesFile))
	if err != nil {
		t.Fatal(err)
	}
	want := `acme/acme-api:
  project: acme
  dir: acme-api
acme/acme-mobile:
  project: acme
  dir: acme-mobile
acme/acme-web:
  project: acme
  dir: web
`
	if string(data) != want {
		t.Errorf("remotes.yaml =\n%s\nwant\n%s", data, want)
	}
}

func TestWriteRemotesWithoutGitHubReposWritesNothing(t *testing.T) {
	brain := filepath.Join(t.TempDir(), ".wong")

	if err := WriteRemotes(brain, Registry{"side": {t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(brain, RemotesFile)); !os.IsNotExist(err) {
		t.Errorf("remotes.yaml exists: %v", err)
	}
}
