package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/graph"
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/remote"
)

// NoteWritten answers PostToolUse on a file of the brain with what wong needs to check it and sync it, for the agent to fix at once.
func NoteWritten(in Input, env Env) string {
	if in.ToolInput.FilePath == "" || env.Brain == "" {
		return ""
	}
	rel, err := filepath.Rel(filepath.Clean(env.Brain), filepath.Clean(in.ToolInput.FilePath))
	rel = filepath.ToSlash(rel)
	if err != nil || !remote.Synced(rel) {
		return ""
	}
	data, err := os.ReadFile(in.ToolInput.FilePath)
	if err != nil {
		return ""
	}
	var fixes []string
	kept, err := remote.Kept(rel, data)
	if err != nil {
		fixes = append(fixes, err.Error())
	}
	if leak := remote.Leaks(rel, data); leak != "" && !kept {
		fixes = append(fixes, "wong will not push this file to the server while "+leak)
	}
	if project, scope, ok := shelfOf(rel); ok {
		if n, err := note.Parse(data); err != nil {
			fixes = append(fixes, fmt.Sprintf("its frontmatter does not parse (%v), so wong leaves it out", err))
		} else {
			fixes = append(fixes, lint(n, rel, scope, project, in.ToolName == "Write", env)...)
		}
	}
	if len(fixes) == 0 {
		return ""
	}
	reason := fmt.Sprintf("wong: %s is saved, but needs this so wong can check it and sync it. Fix it now, in one edit; wong's fields go at the top level "+
		"of the frontmatter, beside name and description, not under metadata:\n- %s\n", rel, strings.Join(fixes, "\n- "))
	out, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
	return string(out)
}

// shelfOf returns the project and scope of a note on a shelf, me or a project's folder, and false for any other file.
func shelfOf(rel string) (string, string, bool) {
	if path.Ext(rel) != ".md" || path.Base(rel) == "MEMORY.md" {
		return "", "", false
	}
	shelf := path.Dir(rel)
	if shelf == "me" {
		return "", "global", true
	}
	project, ok := strings.CutPrefix(shelf, "projects/")
	if !ok || strings.Contains(project, "/") {
		return "", "", false
	}
	return project, "project:" + project, true
}

// lint lists what a note lacks or gets wrong in wong's format, checked against the rest of the brain.
func lint(n *note.Note, rel, scope, project string, created bool, env Env) []string {
	var fixes []string
	if n.Kind == "" {
		fix := "add `kind`: preference, project, person, client, decision or reference"
		if k := note.KindOf(n.ClaudeType()); k != "" {
			fix += fmt.Sprintf(" (its metadata.type says %s)", k)
		}
		fixes = append(fixes, fix)
	}
	if n.Scope != scope {
		fixes = append(fixes, fmt.Sprintf("set `scope: %s`", scope))
	}
	if err := note.CheckPending(n.Pending); err != nil {
		fixes = append(fixes, err.Error())
	}
	g, err := graph.Load(env.Brain)
	if err != nil {
		return fixes
	}
	others := graph.New(slices.DeleteFunc(slices.Clone(g.Notes), func(o graph.Note) bool { return o.Path == rel }))
	if len(n.Tags) == 0 {
		fix := "add a few `tags` for what it is about"
		if used := others.TagList(20); len(used) > 0 {
			fix += ", reusing those in use: " + strings.Join(used, ", ")
		}
		fixes = append(fixes, fix)
	} else if tags, err := others.CheckTags(n.Tags); err != nil {
		fixes = append(fixes, err.Error())
	} else if !slices.Equal(tags, n.Tags) {
		fixes = append(fixes, "write the tags as ["+strings.Join(tags, ", ")+"]")
	}
	fixes = append(fixes, g.Missing(n.Description+"\n"+n.Body, "")...)
	if created {
		for _, o := range g.Similar(graph.Note{Path: rel, Note: n}) {
			fixes = append(fixes, fmt.Sprintf("%s (%s) seems to cover the same subject: if it does, add what is new to it and delete this one", o.Path, o.Description))
		}
	}
	var declared []string
	for _, o := range g.Notes {
		declared = append(declared, o.Sources...)
	}
	if sources := check.Undeclared(n, env.Repos[project], env.Home, check.KnownTeams(env.Brain, declared)); len(sources) > 0 {
		fixes = append(fixes, "declare what it cites in `sources`, so wong notices when it changes: "+strings.Join(sources, ", "))
	}
	return fixes
}
