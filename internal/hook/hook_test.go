package hook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/repos"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T) (Env, string) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
	} {
		t.Setenv(k, v)
	}
	home := t.TempDir()
	repo := filepath.Join(home, "acme", "acme-api")
	run(t, home, "init", "-q", repo)
	brain := filepath.Join(home, ".wong")
	write(t, filepath.Join(brain, "me", "MEMORY.md"), "- [Use uv](use-uv.md) — uv run, never pip\n")
	write(t, filepath.Join(brain, "projects", "acme", "MEMORY.md"), "- [A](a.md) — a\n")
	write(t, filepath.Join(brain, "projects", "acme", "a.md"), "---\nname: a\ndescription: d\n---\nSee [[gone]].\n")
	return Env{Brain: brain, Home: home, Repos: repos.Registry{"acme": {repo}}}, repo
}

func TestSessionStartInALinkedRepo(t *testing.T) {
	env, repo := newEnv(t)

	got := SessionStart(context.Background(), Input{Cwd: filepath.Join(repo)}, env)

	for _, want := range []string{
		`wong: this session's memory is the brain's project "acme" (~/.wong/projects/acme).`,
		"the notes are in ~/.wong/me",
		"- [Use uv](use-uv.md) — uv run, never pip",
		"1 findings say notes of this project's memory contradict their sources",
		"wong:maintain-brain",
		"- projects/acme/a.md: [[gone]] links to a note that does not exist",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}
}

func TestSessionStartInAnUnlinkedRepo(t *testing.T) {
	env, _ := newEnv(t)
	other := filepath.Join(env.Home, "other")
	run(t, env.Home, "init", "-q", other)

	got := SessionStart(context.Background(), Input{Cwd: other}, env)

	if !strings.Contains(got, "this repo is not linked to the brain") || !strings.Contains(got, "(in the brain: acme)") {
		t.Errorf("context = %q", got)
	}
}

func TestSessionStartOutsideARepoAddsNothing(t *testing.T) {
	env, _ := newEnv(t)

	if got := SessionStart(context.Background(), Input{Cwd: env.Home}, env); got != "" {
		t.Errorf("context = %q", got)
	}
}

func TestSessionEndCommitsTheBrain(t *testing.T) {
	env, repo := newEnv(t)
	run(t, env.Brain, "init", "-q")
	run(t, env.Brain, "add", "-A")
	run(t, env.Brain, "commit", "-q", "-m", "start")

	if err := SessionEnd(Input{SessionID: "0123456789ab", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}
	if got := run(t, env.Brain, "log", "--format=%s"); got != "start" {
		t.Errorf("a session that changed nothing committed: %q", got)
	}

	write(t, filepath.Join(env.Brain, "projects", "acme", "b.md"), "---\nname: b\ndescription: d\n---\nNew.\n")
	if err := SessionEnd(Input{SessionID: "0123456789ab", Cwd: repo}, env); err != nil {
		t.Fatal(err)
	}
	if got := run(t, env.Brain, "log", "-1", "--format=%s"); got != "session 01234567 in acme" {
		t.Errorf("last commit = %q", got)
	}
}

func TestSessionEndIgnoresABrainWithoutGit(t *testing.T) {
	env, repo := newEnv(t)

	if err := SessionEnd(Input{SessionID: "s", Cwd: repo}, env); err != nil {
		t.Errorf("SessionEnd() = %v", err)
	}
}
