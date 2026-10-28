// Package hook answers Claude Code's session hooks: context when a session starts, a brain snapshot when it ends.
package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/graph"
	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/remote"
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
	// Remote is this machine's wong server, or nil when it has none.
	Remote *remote.Client
}

// SessionStart pulls the brain from the server, if there is one, and returns the context to add to a new session.
func SessionStart(ctx context.Context, in Input, env Env) string {
	var b strings.Builder
	if env.Remote != nil {
		if _, err := remote.Pull(ctx, env.Remote, env.Brain); err != nil {
			fmt.Fprintln(os.Stderr, "wong: pull:", err)
		}
		if st, err := remote.LoadState(env.Brain); err == nil && len(st.Conflicts) > 0 {
			fmt.Fprintf(&b, "wong: %d notes of the brain changed both here and on another machine; the server's versions are in %s. "+
				"Compare them, keep or merge, then run `wong resolve <path> --keep local|server` (ask the user if unsure).\n\n",
				len(st.Conflicts), link.MemoryDir(env.Brain, env.Home, ".wong/conflicts"))
		}
	}
	dir, isRepo := toplevel(in.Cwd)
	project := env.Repos.ProjectOf(dir)
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
	if g, err := graph.Load(env.Brain); err == nil {
		if tags := g.TagList(40); len(tags) > 0 {
			fmt.Fprintf(&b, "\nTags in use in the brain; reuse them when you tag a note, never a new spelling of one: %s.\n", strings.Join(tags, ", "))
		}
	}

	var findings []check.Finding
	if rep, err := check.Run(ctx, check.Options{Brain: env.Brain, Repos: env.Repos, Tracker: env.Tracker, Project: project}); err == nil {
		findings = rep.Findings
		if env.Remote != nil {
			if err := env.Remote.ReportFindings(ctx, project, rep.Findings); err != nil {
				fmt.Fprintln(os.Stderr, "wong: findings:", err)
			}
		}
	}
	list := fmt.Sprintf("`wong check --project %s` lists everything", project)
	if env.Remote != nil {
		tasks, err := env.Remote.Tasks(ctx, project)
		if err != nil {
			fmt.Fprintln(os.Stderr, "wong: tasks:", err)
		}
		for _, t := range tasks {
			if !slices.ContainsFunc(findings, func(f check.Finding) bool { return f.Note == t.Note && f.Ref == t.Ref }) {
				findings = append(findings, check.Finding{Note: t.Note, Severity: check.Severity(t.Severity), Ref: t.Ref, Message: t.Message})
			}
		}
		list = fmt.Sprintf("`wong check --project %s` and `wong tasks --project %s` list everything", project, project)
	}
	var stale []check.Finding
	review := map[string]bool{}
	for _, f := range findings {
		if f.Severity == check.Stale {
			stale = append(stale, f)
		} else {
			review[f.Note] = true
		}
	}
	if len(stale) > 0 {
		fmt.Fprintf(&b, "\nwong check: %d findings say notes of this project's memory contradict their sources. "+
			"When you have finished what the user asked, fix them with the wong:maintain-brain skill, one note at a time "+
			"(%s):\n", len(stale), list)
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

// SessionEnd records the user's skills and the projects' GitHub repos, commits whatever changed in the brain and pushes it to the server.
func SessionEnd(ctx context.Context, in Input, env Env) error {
	_, saveErr := skills.Save(skills.Env{Brain: env.Brain, Home: env.Home, Repos: env.Repos})
	err := errors.Join(saveErr, repos.WriteRemotes(env.Brain, env.Repos), commit(in, env))
	if env.Remote != nil {
		if _, pushErr := remote.Push(ctx, env.Remote, env.Brain); pushErr != nil {
			err = errors.Join(err, pushErr)
		}
	}
	return err
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
