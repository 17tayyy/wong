package skills

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSave(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	home := t.TempDir()
	brain := filepath.Join(home, ".wong")
	write(t, filepath.Join(home, ".agents", ".skill-lock.json"), `{"version": 3, "skills": {"orca-cli": {
		"source": "acme/orca", "sourceType": "github", "sourceUrl": "https://github.com/acme/orca.git",
		"skillPath": "skills/orca-cli/SKILL.md", "skillFolderHash": "abc", "installedAt": "2026-07-29"}}}`)
	write(t, filepath.Join(home, ".agents", "skills", "orca-cli", "SKILL.md"), "orca")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/orca-cli", filepath.Join(home, ".claude", "skills", "orca-cli")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".claude", "skills", "own", "SKILL.md"), "mine")
	write(t, filepath.Join(home, ".claude", "skills", "own", "references", "a.md"), "a")
	write(t, filepath.Join(home, ".claude", "skills", "synced", "x", "SKILL.md"), "synced by claude.ai")

	repo := filepath.Join(home, "shop")
	git(t, home, "init", "-q", repo)
	write(t, filepath.Join(repo, ".claude", "skills", "shadcn", "SKILL.md"), "untracked")
	write(t, filepath.Join(repo, ".agents", "skills", "seo", "SKILL.md"), "tracked")
	write(t, filepath.Join(repo, ".agents", "skills", "listed", "SKILL.md"), "in the repo's lock")
	write(t, filepath.Join(repo, "skills-lock.json"), `{"version": 1, "skills": {"listed": {"source": "acme/listed", "sourceType": "github"}}}`)
	git(t, repo, "add", ".agents/skills/seo")
	git(t, repo, "commit", "-q", "-m", "seo")

	res, err := Save(Env{Brain: brain, Home: home, Repos: map[string][]string{"shop": {repo}}})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(res.Sourced, []string{"orca-cli", "shop/shop/listed"}) {
		t.Errorf("Sourced = %v", res.Sourced)
	}
	if want := []string{"skills/own", "skills/projects/shop/shop/shadcn"}; !slices.Equal(res.Copied, want) {
		t.Errorf("Copied = %v, want %v", res.Copied, want)
	}
	var lock Lock
	data, _ := os.ReadFile(filepath.Join(brain, "skills", "skills-lock.json"))
	if err := json.Unmarshal(data, &lock); err != nil || lock.Skills["orca-cli"].Source != "acme/orca" || lock.Skills["orca-cli"].SkillPath != "skills/orca-cli/SKILL.md" {
		t.Errorf("lock = %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(brain, "skills", "projects", "shop", "shop", "skills-lock.json")); err != nil {
		t.Errorf("the repo's untracked lock was not recorded: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(brain, "skills", "own", "references", "a.md")); string(got) != "a" {
		t.Errorf("copied reference = %q", got)
	}

	if err := os.Remove(filepath.Join(home, ".claude", "skills", "own", "references", "a.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(Env{Brain: brain, Home: home}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(brain, "skills", "own", "references", "a.md")); !os.IsNotExist(err) {
		t.Errorf("a file removed from the skill stayed in the brain: %v", err)
	}

	other := t.TempDir()
	if _, err := Save(Env{Brain: brain, Home: other}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(other, ".agents", ".skill-lock.json"), `{"version": 3, "skills": {"seo": {"source": "acme/seo", "sourceType": "github"}}}`)
	if _, err := Save(Env{Brain: brain, Home: other}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(brain, "skills", "skills-lock.json"))
	lock = Lock{}
	if err := json.Unmarshal(data, &lock); err != nil || lock.Skills["orca-cli"].Source != "acme/orca" || lock.Skills["seo"].Source != "acme/seo" {
		t.Errorf("a machine with other skills changed what the brain records: %s, %v", data, err)
	}
}
