package repos

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInMainMapsALinkedWorktreeToTheMainCheckout(t *testing.T) {
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
	} {
		t.Setenv(k, v)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root := t.TempDir()
	main := filepath.Join(root, "acme")
	git(root, "init", "-q", main)
	git(main, "commit", "-q", "--allow-empty", "-m", "init")
	nested := filepath.Join(main, ".claude", "worktrees", "feature")
	git(main, "worktree", "add", "-q", nested)
	apart := filepath.Join(root, "acme-feature")
	git(main, "worktree", "add", "-q", apart)

	for path, want := range map[string]string{
		nested:                                   main,
		filepath.Join(nested, "app", "x.py"):     filepath.Join(main, "app", "x.py"),
		filepath.Join(apart, "new", "file.go"):   filepath.Join(main, "new", "file.go"),
		filepath.Join(main, "app", "x.py"):       filepath.Join(main, "app", "x.py"),
		filepath.Join(root, "elsewhere", "a.md"): filepath.Join(root, "elsewhere", "a.md"),
	} {
		if got := InMain(path); got != want {
			t.Errorf("InMain(%s) = %s, want %s", path, got, want)
		}
	}
}
