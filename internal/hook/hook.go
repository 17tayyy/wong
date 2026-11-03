// Package hook answers the hooks of Claude Code, Codex, Cursor and OpenCode: context when a session starts, notes on the files an agent touches, a check of each note it writes, and a brain snapshot when it ends.
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
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/repos"
	"github.com/17tayyy/wong/internal/skills"
)

const (
	maxFindings = 5
	maxReview   = 15
	maxMaintain = 3
	maxIndex    = 150
	indexMark   = "\x00index\x00"
)

// Input is the part of a hook's JSON input that wong reads.
type Input struct {
	SessionID      string   `json:"session_id"`
	Cwd            string   `json:"cwd"`
	WorkspaceRoots []string `json:"workspace_roots"`
	CursorVersion  string   `json:"cursor_version"`
	Source         string   `json:"source"`
	ToolName       string   `json:"tool_name"`
	ToolInput      struct {
		FilePath         string `json:"file_path"`
		FilePathCamel    string `json:"filePath"`
		Path             string `json:"path"`
		Command          string `json:"command"`
		PatchText        string `json:"patchText"`
		WorkingDirectory string `json:"working_directory"`
		Workdir          string `json:"workdir"`
	} `json:"tool_input"`
}

type Env struct {
	Agent   Agent
	Brain   string
	Home    string
	Repos   repos.Registry
	Tracker check.Tracker
	// Remote is this machine's wong server, or nil when it has none.
	Remote *remote.Client
}

// SessionStart pulls the brain from the server, if there is one, and returns the context to add to a new session, in the agent's format.
func SessionStart(ctx context.Context, in Input, env Env) string {
	return answer(env.Agent, "SessionStart", sessionStart(ctx, in, env))
}

func sessionStart(ctx context.Context, in Input, env Env) string {
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
		if st, err := env.Remote.Standing(ctx); err == nil {
			if st.Moved != "" {
				fmt.Fprintf(&b, "wong: the user moved their brain to %s, but this machine still syncs with the old server. Tell the user to run "+
					"`wong setup %s` here, and to approve this machine from one that has the key.\n\n", st.Moved, st.Moved)
			}
			if st.Notice != "" {
				fmt.Fprintf(&b, "wong: about the user's wong account: %s Tell the user at the start of your answer.\n\n", strings.TrimSpace(st.Notice+" "+st.Link))
			}
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
			outside := "Claude Code keeps its memory outside it"
			if !env.Agent.hasMemory() {
				outside = "none of the user's project notes apply here yet"
			}
			fmt.Fprintf(&b, "wong: this repo is not linked to the brain, so %s. "+
				"If it belongs to a project (in the brain: %s), link it with `wong link --project <id>` and restart the session.\n",
				outside, strings.Join(projects(env.Brain), ", "))
		}
		if !env.Agent.hasMemory() {
			globalIndex(&b, env)
			saving(&b, env, "me", "global")
		}
		return b.String()
	}

	if !env.Agent.hasMemory() {
		fmt.Fprintf(&b, "wong: the brain's project %q (%s) is your memory in this session, shared with every agent the user works with. "+
			"Its notes, one a line; read the ones that bear on your task:\n%s\n",
			project, link.MemoryDir(env.Brain, env.Home, "projects/"+project), indexMark)
	} else {
		fmt.Fprintf(&b, "wong: this session's memory is the brain's project %q (%s).\n",
			project, link.MemoryDir(env.Brain, env.Home, "projects/"+project))
	}
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
	globalIndex(&b, env)
	saving(&b, env, project, "project:"+project)

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
			"When you have finished what the user asked, fix them with the %s skill, one note at a time "+
			"(%s):\n", says, skill(env.Agent), list)
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
				"sources and fix it before relying on it; otherwise maintain it with the %s skill when you have finished "+
				"what the user asked.\n", names[0], skill(env.Agent))
		} else {
			fmt.Fprintf(&b, "\nwong check: these %d notes of this project may be outdated: %s%s. If one bears on your task, check it against "+
				"its sources and fix it before relying on it. When you have finished what the user asked, maintain up to %d of the others "+
				"with the %s skill.\n", len(review), strings.Join(names, ", "), more, maxMaintain, skill(env.Agent))
		}
	}
	text := b.String()
	if env.Agent.hasMemory() {
		return text
	}
	budget := 0
	if env.Agent == Cursor {
		budget = max(1, maxContext-(len(text)-len(indexMark)))
	}
	return strings.Replace(text, indexMark, index(env.Brain, "projects/"+project, env.Home, budget), 1)
}

// globalIndex adds the index of me/, the notes that apply in every repo.
func globalIndex(b *strings.Builder, env Env) {
	if index, err := os.ReadFile(filepath.Join(env.Brain, "me", "MEMORY.md")); err == nil && len(strings.TrimSpace(string(index))) > 0 {
		fmt.Fprintf(b, "\nGlobal preferences from the brain; the notes are in %s, read one when it applies:\n%s\n",
			link.MemoryDir(env.Brain, env.Home, "me"), strings.TrimSpace(string(index)))
	}
}

// saving tells the agent how a note of the brain is written: where, for an agent with no memory of its own, and in what format.
func saving(b *strings.Builder, env Env, shelf, scope string) {
	if !env.Agent.hasMemory() {
		fmt.Fprintf(b, "\nYou have no memory of your own between sessions: the brain is it. When you learn something worth keeping "+
			"(what the user prefers or corrects, a decision and why, how something works that the code and its history do not say), "+
			"look for a note on it with `wong note search <words>` and update it, or write a new one with `wong note write --to %s` "+
			"(see `wong note write --help`). Never save what the repo already records, or what only matters to this session.\n", shelf)
	}
	fmt.Fprintf(b, "\nWhen you save a note in the brain, its frontmatter carries at the top level, beside name and description and not under "+
		"metadata: `kind` (%s; a pitfall is what was tried and failed, or what is fragile, in the files it cites), "+
		"`scope` (`%s` here, `global` in me/), `sources` for what it relies on "+
		"(file:<repo>/<path>, commit:<repo>@<sha>, linear:<ID>, note:<name> or a URL), a few `tags`, `applies_to` with globs from "+
		"a repo's root (`app/billing/**`, `*.sql`) when it holds for every file there, `pending: release` or "+
		"`pending: main` when it leaves work for the next release or push to the main branch, and `visibility: local` when it must "+
		"stay on this machine, never pushed to the server. Point to sources instead of copying their state.\n", note.KindNames, scope)
	if g, err := graph.Load(env.Brain); err == nil {
		if tags := g.TagList(40); len(tags) > 0 {
			fmt.Fprintf(b, "\nTags in use in the brain; reuse them when you tag a note, never a new spelling of one: %s.\n", strings.Join(tags, ", "))
		}
	}
}

// index returns a shelf's MEMORY.md, cut short past maxIndex lines or, with a budget, past that many bytes.
func index(brain, shelf, home string, budget int) string {
	data, _ := os.ReadFile(filepath.Join(brain, shelf, "MEMORY.md"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "(no notes yet)"
	}
	where := link.MemoryDir(brain, home, shelf+"/MEMORY.md")
	for keep := min(len(lines), maxIndex); ; keep-- {
		out := lines[:keep:keep]
		switch more := len(lines) - keep; {
		case keep == 0:
			out = append(out, fmt.Sprintf("- all %d are in %s", more, where))
		case more > 0:
			out = append(out, fmt.Sprintf("- and %d more in %s", more, where))
		}
		if s := strings.Join(out, "\n"); budget <= 0 || len(s) <= budget || keep == 0 {
			return s
		}
	}
}

// SessionEnd records the user's skills and the projects' GitHub repos, commits whatever changed in the brain and pushes it to the server;
// OpenCode runs it at the end of every turn, so its sessions keep track of the notes they were shown.
func SessionEnd(ctx context.Context, in Input, env Env) error {
	if env.Agent != OpenCode {
		forget(env.Brain, in.SessionID)
	}
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

// short cuts a session's id to 8 characters, taking the random end of OpenCode's, whose start is the time.
func short(id string) string {
	if rest, ok := strings.CutPrefix(id, "ses_"); ok && len(rest) > 8 {
		return rest[len(rest)-8:]
	}
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
