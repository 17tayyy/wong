package mcp

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/graph"
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/repos"
)

type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
	run         func(s *Server, machine string, args json.RawMessage) (string, error)
}

var errArgs = errors.New("invalid arguments")

func argErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errArgs, fmt.Sprintf(format, args...))
}

func object(required []string, props map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

var (
	readOnly    = map[string]any{"readOnlyHint": true, "openWorldHint": false}
	writes      = map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}
	destructive = map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false}
)

const (
	noteArg   = "The note: its path (projects/acme/deploy.md), its name, or its file name without .md."
	shelfArg  = "me for a note that applies everywhere, or a project id."
	sourceArg = "Where a claim comes from: linear:<ID>, file:<repo>/<path>, commit:<repo>@<sha>, note:<name> or a URL."
)

var tools = []tool{
	{
		Name:  "get_context",
		Title: "Load the brain for a task",
		Description: "Start here. Returns the user's global preferences (me/MEMORY.md), the project's index of notes " +
			"(projects/<id>/MEMORY.md), the tags in use, and what is open on its notes: findings from wong check and tasks " +
			"from GitHub and Linear webhooks. Name the project by its id, or by the git remote of the repo you work in; with " +
			"neither you get the preferences and the list of projects.",
		InputSchema: object(nil, map[string]any{
			"project": str("The project id, as in projects/<id>/."),
			"repo":    str("The repo you work in: its git remote URL or owner/name."),
		}),
		Annotations: readOnly,
		run:         getContext,
	},
	{
		Name:        "search_notes",
		Title:       "Search notes",
		Description: "Find notes whose name, description or text contain every word of the query, in one project or the whole brain.",
		InputSchema: object([]string{"query"}, map[string]any{
			"query":   str("Words to look for; a note matches when it contains all of them."),
			"project": str("Search only this project's notes, plus me."),
		}),
		Annotations: readOnly,
		run:         searchNotes,
	},
	{
		Name:  "read_note",
		Title: "Read a note",
		Description: "Return a note's whole file (frontmatter and body), its path, its sha for edit_note, what is open on it, " +
			"and the notes it relates to but does not link yet. Read a note whole, description included, before changing it.",
		InputSchema: object([]string{"note"}, map[string]any{"note": str(noteArg)}),
		Annotations: readOnly,
		run:         readNote,
	},
	{
		Name:  "write_note",
		Title: "Write a new note",
		Description: "Create a note and add it to its shelf's MEMORY.md. Point to the source instead of copying its state: " +
			"\"tracked in ENG-15\", not \"ENG-15 is in progress\"; past facts with a date are fine. List the sources the note " +
			"relies on, reuse the tags in use (get_context lists them) and link related notes with [[name]]. It is refused when a " +
			"tag is a spelling of one in use, when a [[link]] reaches no note, and when a note on the same shelf already covers " +
			"the subject: update that one with edit_note, or set distinct if the subject is really different. The answer lists " +
			"related notes to link.",
		InputSchema: object([]string{"to", "name", "description", "kind", "body"}, map[string]any{
			"to":          str(shelfArg),
			"name":        str("A short name; the file name is made from it."),
			"description": str("One line: what the note says."),
			"kind":        map[string]any{"type": "string", "enum": []string{"preference", "project", "person", "client", "decision", "reference"}, "description": "What the note is: a preference of the user's, project knowledge, a decision, a reference, or about a person or client."},
			"body":        str("The note's text, in markdown. Link other notes with [[name]], only to notes that exist."),
			"sources":     strs(sourceArg),
			"tags":        strs("Tags for the note: reuse the tags in use; lowercase, words joined by -, nested with /."),
			"distinct":    map[string]any{"type": "boolean", "description": "Set it only when write_note said a note covers the same subject and you checked that it does not."},
		}),
		Annotations: writes,
		run:         writeNote,
	},
	{
		Name:  "edit_note",
		Title: "Edit a note",
		Description: "Change a note: old_text must appear exactly once in its file, frontmatter included, and becomes new_text. " +
			"To replace the whole file, pass content and the sha read_note returned. Keep the note's meaning, voice and language, " +
			"and change only what the evidence contradicts. New tags must reuse the tags in use and new [[links]] must reach a " +
			"note. Then mark it with verify_note if every claim you could check holds.",
		InputSchema: object([]string{"note"}, map[string]any{
			"note":     str(noteArg),
			"old_text": str("Text of the file to replace, exactly as read_note shows it."),
			"new_text": str("What replaces old_text; empty to remove it."),
			"content":  str("The whole new file, frontmatter and body, instead of old_text and new_text."),
			"sha":      str("With content: the sha read_note returned, so a change made meanwhile is not lost."),
		}),
		Annotations: writes,
		run:         editNote,
	},
	{
		Name:  "verify_note",
		Title: "Mark a note verified",
		Description: "Add the sources you checked and mark the note verified today. Call it only when every claim you could " +
			"check holds; it is refused while webhook tasks are open on the note.",
		InputSchema: object([]string{"note"}, map[string]any{"note": str(noteArg), "sources": strs(sourceArg)}),
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		run:         verifyNote,
	},
	{
		Name:  "move_note",
		Title: "Move a note to another shelf",
		Description: "Move a note to me or another project, changing its scope and moving its MEMORY.md line. Ask the user " +
			"before moving a note between projects.",
		InputSchema: object([]string{"note", "to"}, map[string]any{"note": str(noteArg), "to": str(shelfArg)}),
		Annotations: writes,
		run:         moveNote,
	},
	{
		Name:  "delete_note",
		Title: "Delete a note",
		Description: "Delete a note and its MEMORY.md line. Ask the user first. It stays in the history, and the user can " +
			"undo it from the wong dashboard.",
		InputSchema: object([]string{"note"}, map[string]any{"note": str(noteArg)}),
		Annotations: destructive,
		run:         deleteNote,
	},
}

var byName = func() map[string]tool {
	m := map[string]tool{}
	for _, t := range tools {
		m[t.Name] = t
	}
	return m
}()

func toolNames() []string {
	return slices.Sorted(maps.Keys(byName))
}

func decode(args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return argErr("%v", err)
	}
	return nil
}

// issues lists what is open on a project's notes, or on one note: findings from checks and tasks from webhooks.
func (s *Server) issues(project, only string) ([]string, error) {
	findings, err := s.store.Findings(project)
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.Tasks(project)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(n, severity, ref, msg, source string) {
		if (only != "" && n != only) || seen[n+"\x00"+ref] {
			return
		}
		seen[n+"\x00"+ref] = true
		out = append(out, fmt.Sprintf("- [%s] %s, %s: %s (from %s)", severity, n, ref, msg, source))
	}
	for _, f := range findings {
		add(f.Note, string(f.Severity), f.Ref, f.Message, "wong check")
	}
	for _, t := range tasks {
		source, _, _ := strings.Cut(t.Source, " ")
		add(t.Note, t.Severity, t.Ref, t.Message, source+" webhook")
	}
	slices.Sort(out)
	return out, nil
}

func projects(b *remote.Brain) map[string]int {
	count := map[string]int{}
	for p := range b.Files {
		if id, ok := remote.NoteProject(p); ok && id != "me" {
			count[id]++
		}
	}
	return count
}

var ownerNameRE = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

// projectOf finds the project of a repo from the remotes.yaml the machines keep in the brain.
func projectOf(b *remote.Brain, repo string) (string, bool) {
	name, ok := repos.GitHubRepo(repo)
	if !ok && ownerNameRE.MatchString(strings.TrimSuffix(repo, ".git")) {
		name, ok = strings.TrimSuffix(repo, ".git"), true
	}
	if !ok {
		return "", false
	}
	remotes, err := repos.ParseRemotes(b.Files[repos.RemotesFile].Content)
	if err != nil {
		return "", false
	}
	r, ok := remotes.Lookup(name)
	return r.Project, ok
}

func getContext(s *Server, _ string, args json.RawMessage) (string, error) {
	var a struct{ Project, Repo string }
	if err := decode(args, &a); err != nil {
		return "", err
	}
	b, err := s.store.Brain()
	if err != nil {
		return "", err
	}
	known := projects(b)
	project := strings.TrimSpace(a.Project)
	var out strings.Builder
	if project == "" && a.Repo != "" {
		if p, ok := projectOf(b, a.Repo); ok {
			project = p
		} else {
			fmt.Fprintf(&out, "No project is linked to the repo %s yet: a machine links it when it runs `wong link` there. Pass a project id from the list below, or write notes for it on a new project id.\n\n", a.Repo)
		}
	}
	if project != "" && known[project] == 0 {
		return "", argErr("there is no project %q in the brain; the projects are: %s", project, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
	}
	fmt.Fprintf(&out, "# Global preferences (me/MEMORY.md)\n\n%s\n", orNone(b.Files["me/MEMORY.md"].Content))
	if project == "" {
		out.WriteString("\n# Projects\n\n")
		for _, id := range slices.Sorted(maps.Keys(known)) {
			fmt.Fprintf(&out, "- %s (%d notes)\n", id, known[id])
		}
		out.WriteString("\nCall get_context with a project or repo to load its notes.\n")
		return out.String(), nil
	}
	fmt.Fprintf(&out, "\n# Project %s (projects/%s/MEMORY.md)\n\n%s\n", project, project, orNone(b.Files["projects/"+project+"/MEMORY.md"].Content))
	if tags := b.Graph().TagList(60); len(tags) > 0 {
		fmt.Fprintf(&out, "\n# Tags in use\n\nReuse these instead of new spellings: %s.\n", strings.Join(tags, ", "))
	}
	open, err := s.issues(project, "")
	if err != nil {
		return "", err
	}
	if len(open) > 0 {
		fmt.Fprintf(&out, "\n# Open on this project's notes\n\nThese notes may be wrong now. Read each with read_note, check the evidence, fix it with edit_note and mark it with verify_note.\n\n%s\n", strings.Join(open, "\n"))
	}
	return out.String(), nil
}

func orNone(data []byte) string {
	if text := strings.TrimSpace(string(data)); text != "" {
		return text
	}
	return "(no notes yet)"
}

func searchNotes(s *Server, _ string, args json.RawMessage) (string, error) {
	var a struct{ Query, Project string }
	if err := decode(args, &a); err != nil {
		return "", err
	}
	words := strings.Fields(strings.ToLower(a.Query))
	if len(words) == 0 {
		return "", argErr("query is empty")
	}
	b, err := s.store.Brain()
	if err != nil {
		return "", err
	}
	type hit struct {
		path, name, desc, line string
		inName                 bool
	}
	var hits []hit
	for _, p := range slices.Sorted(maps.Keys(b.Files)) {
		id, isNote := remote.NoteProject(p)
		if !isNote || (a.Project != "" && id != a.Project && id != "me") {
			continue
		}
		text := string(b.Files[p].Content)
		lower := strings.ToLower(text)
		if slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(lower, w) }) {
			continue
		}
		h := hit{path: p, name: strings.TrimSuffix(path.Base(p), ".md")}
		if n, err := note.Parse(b.Files[p].Content); err == nil {
			h.name, h.desc = n.Name, n.Description
			for _, l := range strings.Split(n.Body, "\n") {
				if strings.Contains(strings.ToLower(l), words[0]) {
					h.line = strings.TrimSpace(l)
					break
				}
			}
		}
		h.inName = strings.Contains(strings.ToLower(h.name+" "+h.desc), words[0])
		hits = append(hits, h)
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No note contains all of: %s.", strings.Join(words, ", ")), nil
	}
	slices.SortStableFunc(hits, func(x, y hit) int { return cmp.Compare(boolRank(y.inName), boolRank(x.inName)) })
	var out strings.Builder
	fmt.Fprintf(&out, "%d notes match", len(hits))
	if len(hits) > 20 {
		fmt.Fprintf(&out, "; the first 20")
		hits = hits[:20]
	}
	out.WriteString(":\n")
	for _, h := range hits {
		fmt.Fprintf(&out, "\n- %s: %s", h.path, h.name)
		if h.desc != "" && h.desc != h.name {
			fmt.Fprintf(&out, " — %s", h.desc)
		}
		if h.line != "" {
			fmt.Fprintf(&out, "\n  > %s", truncate(h.line, 200))
		}
	}
	return out.String() + "\n", nil
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func readNote(s *Server, _ string, args json.RawMessage) (string, error) {
	var a struct{ Note string }
	if err := decode(args, &a); err != nil {
		return "", err
	}
	b, err := s.store.Brain()
	if err != nil {
		return "", err
	}
	p, ok := b.Resolve(a.Note)
	if !ok {
		return "", argErr("there is no note %q; search_notes finds notes by their words", a.Note)
	}
	f := b.Files[p]
	project, _ := remote.NoteProject(p)
	open, err := s.issues(project, p)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "path: %s\nsha: %s\n\n%s", p, f.Sha, f.Content)
	if len(open) > 0 {
		fmt.Fprintf(&out, "\nOpen on this note:\n%s\n", strings.Join(open, "\n"))
	}
	if n, err := note.Parse(f.Content); err == nil {
		if related := b.Graph().Related(graph.Note{Path: p, Note: n}, 5); len(related) > 0 {
			fmt.Fprintf(&out, "\nRelated but not linked yet; link the ones that help with [[name]]:\n- %s\n", strings.Join(related, "\n- "))
		}
	}
	return out.String(), nil
}

func writeNote(s *Server, machine string, args json.RawMessage) (string, error) {
	var a struct {
		To, Name, Description, Kind, Body string
		Sources, Tags                     []string
		Distinct                          bool
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	kind, err := note.ParseKind(a.Kind)
	if err != nil {
		return "", argErr("%v", err)
	}
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Description) == "" || strings.TrimSpace(a.Body) == "" {
		return "", argErr("a note needs a name, a one-line description and a body")
	}
	n := note.New(strings.TrimSpace(a.Name), strings.Join(strings.Fields(a.Description), " "), kind, "", "\n"+strings.TrimSpace(a.Body)+"\n")
	n.Sources, n.Tags = a.Sources, a.Tags
	checks := remote.Agent
	checks.Duplicates = !a.Distinct
	p, _, err := s.store.CreateNote(machine, a.To, n, checks)
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("Wrote %s and added it to %s/MEMORY.md.", p, path.Dir(p))
	if b, err := s.store.Brain(); err == nil {
		if related := b.Graph().Related(graph.Note{Path: p, Note: n}, 5); len(related) > 0 {
			out += "\n\nRelated notes it does not link yet; link the ones that help with edit_note:\n- " + strings.Join(related, "\n- ")
		}
	}
	return out, nil
}

func editNote(s *Server, machine string, args json.RawMessage) (string, error) {
	var a struct {
		Note    string
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
		Content string
		Sha     string
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	b, err := s.store.Brain()
	if err != nil {
		return "", err
	}
	p, ok := b.Resolve(a.Note)
	if !ok {
		return "", argErr("there is no note %q; search_notes finds notes by their words", a.Note)
	}
	f := b.Files[p]
	var data string
	switch {
	case a.Content != "":
		if a.Sha == "" {
			return "", argErr("with content, pass the sha read_note returned")
		}
		data = strings.ReplaceAll(a.Content, "\r\n", "\n")
		if !strings.HasSuffix(data, "\n") {
			data += "\n"
		}
	case a.OldText != "":
		text := string(f.Content)
		switch n := strings.Count(text, a.OldText); n {
		case 0:
			return "", argErr("old_text is not in %s; read_note shows the file as it is now", p)
		case 1:
			data = strings.Replace(text, a.OldText, a.NewText, 1)
		default:
			return "", argErr("old_text appears %d times in %s; add the text around it so it appears once", n, p)
		}
		a.Sha = f.Sha
	default:
		return "", argErr("pass old_text and new_text, or content and sha")
	}
	sha, err := s.store.EditNote(machine, p, a.Sha, []byte(data), remote.Checks{Tags: true, Links: true})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved %s (sha %s).", p, sha), nil
}

func verifyNote(s *Server, machine string, args json.RawMessage) (string, error) {
	var a struct {
		Note    string
		Sources []string
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	b, err := s.store.Brain()
	if err != nil {
		return "", err
	}
	p, ok := b.Resolve(a.Note)
	if !ok {
		return "", argErr("there is no note %q", a.Note)
	}
	if _, err := s.store.VerifyNote(machine, p, a.Sources); err != nil {
		return "", err
	}
	return fmt.Sprintf("Marked %s verified today.", p), nil
}

func moveNote(s *Server, machine string, args json.RawMessage) (string, error) {
	var a struct{ Note, To string }
	if err := decode(args, &a); err != nil {
		return "", err
	}
	p, _, err := s.store.MoveNote(machine, a.Note, a.To)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Moved it to %s, with its scope and its MEMORY.md line.", p), nil
}

func deleteNote(s *Server, machine string, args json.RawMessage) (string, error) {
	var a struct{ Note string }
	if err := decode(args, &a); err != nil {
		return "", err
	}
	b, err := s.store.Brain()
	if err != nil {
		return "", err
	}
	p, ok := b.Resolve(a.Note)
	if !ok {
		return "", argErr("there is no note %q", a.Note)
	}
	if _, err := s.store.DeleteNote(machine, p); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted %s and its MEMORY.md line. It stays in the history; the user can undo it from the wong dashboard.", p), nil
}
