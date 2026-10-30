// Package hook answers Claude Code's hooks: context when a session starts, notes on the files an agent touches, a check of each note it writes, and a brain snapshot when it ends.
package hook

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/graph"
	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/repos"
	"github.com/17tayyy/wong/internal/skills"
)

const (
	maxFindings = 5
	maxReview   = 15
	maxMaintain = 3
)

// Input is the part of a hook's JSON input that wong reads.
type Input struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Source    string `json:"source"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
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
		_, pullErr := remote.Pull(ctx, env.Remote, env.Brain)
		if pullErr != nil {
			fmt.Fprintln(os.Stderr, "wong: pull:", pullErr)
		}
		if st, err := remote.LoadState(env.Brain); err == nil && len(st.Conflicts) > 0 {
			fmt.Fprintf(&b, "wong: %d notes of the brain changed both here and on another machine; the server's versions are in %s. "+
				"Compare them, keep or merge, then run `wong resolve <path> --keep local|server` (ask the user if unsure).\n\n",
				len(st.Conflicts), link.MemoryDir(env.Brain, env.Home, ".wong/conflicts"))
		}
		if errors.Is(pullErr, remote.ErrLocked) {
			b.WriteString("wong: the brain is end-to-end encrypted and this machine has no key yet, so it did not sync; it asked for one. " +
				"Tell the user to run `wong keys` here to see this machine's code and `wong keys approve` on a machine that has the key, " +
				"approving it only if it shows the same code, or to run `wong keys recover` here with the recovery code.\n\n")
		}
		if keys, err := env.Remote.Keys(ctx); err == nil && keys.Wrapped != nil && len(keys.Pending) > 0 {
			names := make([]string, len(keys.Pending))
			for i, p := range keys.Pending {
				names[i] = p.Device
			}
			fmt.Fprintf(&b, "wong: %s wait for the key of the end-to-end encrypted brain. Tell the user to run `wong keys approve` "+
				"in a terminal here and to approve each one only if it shows the same code.\n\n", strings.Join(names, ", "))
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
	for _, shelf := range []struct{ dir, who string }{{"projects/" + project, "This project"}, {"me", "me/"}} {
		n := brain.ArchivedIn(env.Brain, shelf.dir)
		if n == 0 {
			continue
		}
		notes := "notes"
		if n == 1 {
			notes = "note"
		}
		fmt.Fprintf(&b, "%s keeps %d archived %s in %s, out of your memory on purpose: read them, or run "+
			"`wong note search --archive <words>`, only when the user asks about older work.\n",
			shelf.who, n, notes, link.MemoryDir(env.Brain, env.Home, shelf.dir+"/"+brain.ArchiveDir))
	}
	if index, err := os.ReadFile(filepath.Join(env.Brain, "me", "MEMORY.md")); err == nil && len(strings.TrimSpace(string(index))) > 0 {
		fmt.Fprintf(&b, "\nGlobal preferences from the brain; the notes are in %s, read one when it applies:\n%s\n",
			link.MemoryDir(env.Brain, env.Home, "me"), strings.TrimSpace(string(index)))
	}
	fmt.Fprintf(&b, "\nWhen you save a note in the brain, its frontmatter carries at the top level, beside name and description and not under "+
		"metadata: `kind` (preference, project, "+
		"person, client, decision or reference), `scope` (`project:%s` here, `global` in me/), `sources` for what it relies on "+
		"(file:<repo>/<path>, commit:<repo>@<sha>, linear:<ID>, note:<name> or a URL), a few `tags`, `pending: release` or "+
		"`pending: main` when it leaves work for the next release or push to the main branch, and `visibility: local` when it must "+
		"stay on this machine, never pushed to the server. Point to sources instead of copying their state.\n", project)
	if g, err := graph.Load(env.Brain); err == nil {
		if tags := g.TagList(40); len(tags) > 0 {
			fmt.Fprintf(&b, "\nTags in use in the brain; reuse them when you tag a note, never a new spelling of one: %s.\n", strings.Join(tags, ", "))
		}
	}

	var findings []check.Finding
	if rep, err := check.Run(ctx, check.Options{Brain: env.Brain, Repos: env.Repos, Tracker: env.Tracker, Project: project}); err == nil {
		findings = rep.Findings
		if env.Remote != nil {
			if err := env.Remote.ReportFindings(ctx, project, shared(env.Brain, rep.Findings)); err != nil {
				fmt.Fprintln(os.Stderr, "wong: findings:", err)
			}
		}
	}
	list := fmt.Sprintf("`wong check --project %s` lists everything", project)
	if env.Remote != nil {
		tasks, err := remote.OpenTasks(ctx, env.Remote, env.Brain, project)
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
	var deploys, stale []check.Finding
	review := map[string]bool{}
	for _, f := range findings {
		switch {
		case f.Deploy():
			deploys = append(deploys, f)
		case f.Severity == check.Stale:
			stale = append(stale, f)
		default:
			review[f.Note] = true
		}
	}
	if len(deploys) > 0 {
		b.WriteString("\nwong: notes of this project leave work for a deploy that has happened. Tell the user about it before " +
			"you start on their request:\n")
		for _, f := range deploys {
			fmt.Fprintf(&b, "- %s: %s\n", f.Note, f.Message)
		}
	}
	if len(stale) > 0 {
		says := fmt.Sprintf("%d findings say", len(stale))
		if len(stale) == 1 {
			says = "1 finding says"
		}
		fmt.Fprintf(&b, "\nwong check: %s notes of this project's memory contradict their sources. "+
			"When you have finished what the user asked, fix them with the wong:maintain-brain skill, one note at a time "+
			"(%s):\n", says, list)
		for i, f := range stale {
			if i == maxFindings {
				fmt.Fprintf(&b, "- and %d more\n", len(stale)-maxFindings)
				break
			}
			fmt.Fprintf(&b, "- %s: %s %s\n", f.Note, f.Ref, f.Message)
		}
	}
	if len(review) > 0 {
		names := slices.Sorted(maps.Keys(review))
		for i, n := range names {
			names[i] = filepath.Base(n)
		}
		more := ""
		if len(names) > maxReview {
			names, more = names[:maxReview], fmt.Sprintf(" and %d more", len(names)-maxReview)
		}
		if len(review) == 1 {
			fmt.Fprintf(&b, "\nwong check: this note of this project may be outdated: %s. If it bears on your task, check it against its "+
				"sources and fix it before relying on it; otherwise maintain it with the wong:maintain-brain skill when you have finished "+
				"what the user asked.\n", names[0])
		} else {
			fmt.Fprintf(&b, "\nwong check: these %d notes of this project may be outdated: %s%s. If one bears on your task, check it against "+
				"its sources and fix it before relying on it. When you have finished what the user asked, maintain up to %d of the others "+
				"with the wong:maintain-brain skill.\n", len(review), strings.Join(names, ", "), more, maxMaintain)
		}
	}
	return b.String()
}

// SessionEnd records the user's skills and the projects' GitHub repos, commits whatever changed in the brain and pushes it to the server.
func SessionEnd(ctx context.Context, in Input, env Env) error {
	forget(env.Brain, in.SessionID)
	_, saveErr := skills.Save(skills.Env{Brain: env.Brain, Home: env.Home, Repos: env.Repos})
	err := errors.Join(saveErr, repos.WriteRemotes(env.Brain, env.Repos), commit(in, env))
	if env.Remote != nil {
		res, pushErr := remote.Push(ctx, env.Remote, env.Brain)
		err = errors.Join(err, pushErr)
		for _, r := range res.Refused {
			err = errors.Join(err, fmt.Errorf("%s was not pushed: %s", r.Path, r.Reason))
		}
	}
	return err
}

// shared leaves out the findings on notes kept on this machine, which the server is not to hear of.
func shared(brain string, findings []check.Finding) []check.Finding {
	return slices.DeleteFunc(slices.Clone(findings), func(f check.Finding) bool {
		data, err := os.ReadFile(filepath.Join(brain, f.Note))
		kept, _ := remote.Kept(f.Note, data)
		return err == nil && kept
	})
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
