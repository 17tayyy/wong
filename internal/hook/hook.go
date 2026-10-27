// Package hook answers Claude Code's session hooks: context when a session starts, a brain snapshot when it ends.
package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/repos"
	"github.com/17tayyy/wong/internal/skills"
)

const maxFindings = 5

// Input is the part of a hook's JSON input that wong reads.
type Input struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Source    string `json:"source"`
}

type Env struct {
	Brain   string
	Home    string
	Repos   repos.Registry
	Tracker check.Tracker
}

// SessionStart returns the context to add to a new session.
func SessionStart(ctx context.Context, in Input, env Env) string {
	dir, isRepo := toplevel(in.Cwd)
	project := env.Repos.ProjectOf(dir)
	var b strings.Builder
	if project == "" {
		if isRepo {
			fmt.Fprintf(&b, "wong: this repo is not linked to the brain, so Claude Code keeps its memory outside it. "+
				"If it belongs to a project (in the brain: %s), link it with `wong link --project <id>` and restart the session.\n",
				strings.Join(projects(env.Brain), ", "))
		}
		return b.String()
	}

	fmt.Fprintf(&b, "wong: this session's memory is the brain's project %q (%s).\n",
		project, link.MemoryDir(env.Brain, env.Home, "projects/"+project))
	if index, err := os.ReadFile(filepath.Join(env.Brain, "me", "MEMORY.md")); err == nil && len(strings.TrimSpace(string(index))) > 0 {
		fmt.Fprintf(&b, "\nGlobal preferences from the brain; the notes are in %s, read one when it applies:\n%s\n",
			link.MemoryDir(env.Brain, env.Home, "me"), strings.TrimSpace(string(index)))
	}

	rep, err := check.Run(ctx, check.Options{Brain: env.Brain, Repos: env.Repos, Tracker: env.Tracker, Project: project})
	if err != nil {
		return b.String()
	}
	var stale []check.Finding
	review := map[string]bool{}
	for _, f := range rep.Findings {
		if f.Severity == check.Stale {
			stale = append(stale, f)
		} else {
			review[f.Note] = true
		}
	}
	if len(stale) > 0 {
		fmt.Fprintf(&b, "\nwong check: %d findings say notes of this project's memory contradict their sources. "+
			"When you have finished what the user asked, fix them with the wong:maintain-brain skill, one note at a time "+
			"(`wong check --project %s` lists everything):\n", len(stale), project)
		for i, f := range stale {
			if i == maxFindings {
				fmt.Fprintf(&b, "- and %d more\n", len(stale)-maxFindings)
				break
			}
			fmt.Fprintf(&b, "- %s: %s %s\n", f.Note, f.Ref, f.Message)
		}
	}
	if len(review) > 0 {
		fmt.Fprintf(&b, "\nwong check: %d notes of this project may be outdated; look at them only if the user asks to maintain the brain.\n", len(review))
	}
	return b.String()
}

// SessionEnd records the user's skills and commits whatever changed in the brain, so every session's edits can be undone.
func SessionEnd(in Input, env Env) error {
	_, saveErr := skills.Save(skills.Env{Brain: env.Brain, Home: env.Home, Repos: env.Repos})
	return errors.Join(saveErr, commit(in, env))
}

func commit(in Input, env Env) error {
	if top, ok := toplevel(env.Brain); !ok || top != filepath.Clean(env.Brain) {
		return nil
	}
	status, err := git(env.Brain, "status", "--porcelain")
	if err != nil || status == "" {
		return err
	}
	dir, _ := toplevel(in.Cwd)
	label := env.Repos.ProjectOf(dir)
	if label == "" {
		label = filepath.Base(dir)
	}
	if _, err := git(env.Brain, "add", "-A"); err != nil {
		return err
	}
	_, err = git(env.Brain, "commit", "-q", "-m", fmt.Sprintf("session %s in %s", short(in.SessionID), label))
	return err
}

func toplevel(dir string) (string, bool) {
	if out, err := git(dir, "rev-parse", "--show-toplevel"); err == nil {
		return out, true
	}
	return filepath.Clean(dir), false
}

func projects(brain string) []string {
	entries, _ := os.ReadDir(filepath.Join(brain, "projects"))
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s in %s: %s", args[0], dir, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
