package hook

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionStartInAWorktreeOfALinkedRepo(t *testing.T) {
	env, repo := newEnv(t)
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(repo, ".claude", "worktrees", "feature")
	run(t, repo, "worktree", "add", "-q", worktree)

	got := SessionStart(context.Background(), Input{Cwd: worktree}, env)

	if strings.Contains(got, "not linked to the brain") || !strings.Contains(got, `the brain's project "acme"`) {
		t.Errorf("context = %q", got)
	}
}

func TestFileNotesInAWorktreeCiteThePathInTheRepo(t *testing.T) {
	env, repo := newEnv(t)
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	worktree := filepath.Join(env.Home, "acme", "acme-api-feature")
	run(t, repo, "worktree", "add", "-q", worktree)
	write(t, filepath.Join(env.Brain, "projects/acme/beanie-save.md"), "---\nname: beanie-save\ndescription: about beanie-save\nkind: reference\nscope: project:acme\n---\n"+
		"`save()` in `app/email_domain/models.py` drops fields it does not know.\n")

	got := hookContext(t, FileNotes(fileInput("s1", filepath.Join(worktree, "app", "email_domain", "models.py")), env))

	if !strings.Contains(got, `project "acme" cite app/email_domain/models.py`) || !strings.Contains(got, "- beanie-save (") {
		t.Errorf("context = %q", got)
	}
}
