package remote

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/graph"
	"github.com/17tayyy/wong/internal/note"
)

// Reasons a note operation is refused; NoteError carries one with a message to show as it is.
var (
	ErrNoNote  = errors.New("no such note")
	ErrInvalid = errors.New("invalid note")
	ErrTaken   = errors.New("path taken")
	ErrChanged = errors.New("changed meanwhile")
	ErrSimilar = errors.New("similar note exists")
)

// NoteError is a note operation refused for a reason the user or agent can act on.
type NoteError struct {
	Kind error
	Msg  string
}

func (e *NoteError) Error() string { return e.Msg }
func (e *NoteError) Unwrap() error { return e.Kind }

func refuse(kind error, format string, args ...any) error {
	return &NoteError{kind, fmt.Sprintf(format, args...)}
}

// Brain is the brain as the server holds it at one moment, to find notes and change them.
type Brain struct {
	Files map[string]Change
	names map[string]string
	graph *graph.Graph
}

// Checks are what a write must pass besides a frontmatter that parses: agents get them, people in the dashboard do not.
type Checks struct {
	Tags, Links, Duplicates bool
}

// Agent is every check, for writes made by agents.
var Agent = Checks{Tags: true, Links: true, Duplicates: true}

// Graph sees the brain's notes as a graph of links and tags.
func (b *Brain) Graph() *graph.Graph {
	if b.graph == nil {
		var notes []graph.Note
		for p, f := range b.Files {
			if _, isNote := NoteProject(p); isNote {
				if n, err := note.Parse(f.Content); err == nil {
					notes = append(notes, graph.Note{Path: p, Note: n})
				}
			}
		}
		b.graph = graph.New(notes)
	}
	return b.graph
}

// guard applies the checks to a note about to be written over before, which is nil for a new note.
func (b *Brain) guard(checks Checks, n *note.Note, target string, before *note.Note) error {
	g := b.Graph()
	if checks.Tags {
		var fresh, kept []string
		for _, t := range n.Tags {
			if before != nil && slices.Contains(before.Tags, t) {
				kept = append(kept, t)
				continue
			}
			norm, err := note.NormalizeTag(t)
			if err != nil {
				return refuse(ErrInvalid, "%v.", err)
			}
			if norm != t && before != nil {
				return refuse(ErrInvalid, "Write the tag %q as %q.", t, norm)
			}
			if !slices.Contains(kept, norm) {
				kept = append(kept, norm)
				fresh = append(fresh, norm)
			}
		}
		if _, err := g.CheckTags(fresh); err != nil {
			return refuse(ErrInvalid, "%v. Tags in use: %s.", err, strings.Join(g.TagList(30), ", "))
		}
		n.Tags = kept
	}
	if checks.Links {
		old := ""
		if before != nil {
			old = before.Description + "\n" + before.Body
		}
		if missing := g.Missing(n.Description+"\n"+n.Body, old); len(missing) > 0 {
			return refuse(ErrInvalid, "%s. Link only to notes that exist, by their name or file name; write the missing note first if it is needed.", strings.Join(missing, "; "))
		}
	}
	if checks.Duplicates {
		var same []string
		for _, o := range g.Similar(graph.Note{Path: target, Note: n}) {
			same = append(same, fmt.Sprintf("%s (%q: %s)", o.Path, o.Name, o.Description))
		}
		if len(same) > 0 {
			return refuse(ErrSimilar, "This looks like the same subject as %s. Update that note instead, or mark this one as a different subject.", strings.Join(same, "; "))
		}
	}
	return nil
}

// Brain reads the current files of the brain.
func (s *Store) Brain() (*Brain, error) {
	files, err := s.Files()
	if err != nil {
		return nil, err
	}
	b := &Brain{Files: map[string]Change{}, names: map[string]string{}}
	for _, f := range files {
		b.Files[f.Path] = f
		if _, ok := NoteProject(f.Path); !ok {
			continue
		}
		if n, err := note.Parse(f.Content); err == nil {
			b.names[n.Name] = f.Path
		}
		b.names[strings.TrimSuffix(path.Base(f.Path), ".md")] = f.Path
	}
	return b, nil
}

// Resolve finds a note by its path, its name or its file name.
func (b *Brain) Resolve(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	for _, p := range []string{ref, ref + ".md"} {
		if _, isNote := NoteProject(p); isNote {
			if _, ok := b.Files[p]; ok {
				return p, true
			}
		}
	}
	p, ok := b.names[ref]
	return p, ok
}

// Note returns a note's file and parsed form, refusing a missing note or one whose frontmatter does not parse.
func (b *Brain) Note(ref string) (Change, *note.Note, error) {
	p, ok := b.Resolve(ref)
	if !ok {
		return Change{}, nil, refuse(ErrNoNote, "There is no note %q in the brain.", ref)
	}
	f := b.Files[p]
	n, err := note.Parse(f.Content)
	if err != nil {
		return f, nil, refuse(ErrInvalid, "The frontmatter of %s does not parse (%v). Fix it first.", p, err)
	}
	return f, n, nil
}

func upload(path, base string, data []byte) Upload {
	up := Upload{Path: path, Base: base, Modified: time.Now().UTC().Format(time.RFC3339Nano)}
	if data != nil {
		up.Sha, up.Content = Sha(data), data
	}
	return up
}

// index returns the change edit makes to a folder's MEMORY.md, or false when it makes none.
func (b *Brain) index(folder string, edit func(string) string) (Upload, bool) {
	cur := b.Files[folder+"/MEMORY.md"]
	text := edit(string(cur.Content))
	if text == string(cur.Content) {
		return Upload{}, false
	}
	return upload(folder+"/MEMORY.md", cur.Sha, []byte(text)), true
}

// commit stores the uploads as one operation, or as a plain change when op is empty.
func (s *Store) commit(machine, op string, ups []Upload) error {
	conflicts, err := s.ApplyOp(machine, op, ups)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return refuse(ErrChanged, "%s changed since it was read, so nothing was saved. Read it again and redo the change.", conflicts[0].Path)
	}
	return nil
}

// shelf returns the folder and scope of me or a project id.
func shelf(target string) (string, string, error) {
	folder, scope, err := brain.Folder(target)
	if err != nil {
		return "", "", refuse(ErrInvalid, "%q is not a shelf: use me or a project id.", target)
	}
	return strings.ReplaceAll(folder, "\\", "/"), scope, nil
}

// CreateNote writes a new note on a shelf, me or a project id, and adds it to that shelf's MEMORY.md.
func (s *Store) CreateNote(machine, to string, n *note.Note, checks Checks) (string, string, error) {
	folder, scope, err := shelf(to)
	if err != nil {
		return "", "", err
	}
	n.Scope = scope
	stem, err := brain.Stem(n.Name)
	if err != nil {
		return "", "", refuse(ErrInvalid, "The name needs at least one letter or digit to make a file name from.")
	}
	if bad := slices.IndexFunc(n.Sources, func(s string) bool { return !check.ValidSource(s) }); bad >= 0 {
		return "", "", refuse(ErrInvalid, "Source %q not understood: use linear:<ID>, file:<repo>/<path>, commit:<repo>@<sha>, note:<name> or a URL.", n.Sources[bad])
	}
	data, err := n.Marshal()
	if err != nil {
		return "", "", err
	}
	if _, err := note.Parse(data); err != nil {
		return "", "", refuse(ErrInvalid, "The note is not valid: %v.", err)
	}
	b, err := s.Brain()
	if err != nil {
		return "", "", err
	}
	target := folder + "/" + stem + ".md"
	if _, taken := b.Files[target]; taken {
		return "", "", refuse(ErrTaken, "There is already a note at %s: give this one another name, or edit that one.", target)
	}
	if err := b.guard(checks, n, target, nil); err != nil {
		return "", "", err
	}
	if data, err = n.Marshal(); err != nil {
		return "", "", err
	}
	ups := []Upload{upload(target, "", data)}
	if up, ok := b.index(folder, func(s string) string { return brain.WithIndexEntry(s, stem+".md", n) }); ok {
		ups = append(ups, up)
	}
	op := NewOp("create")
	return target, op, s.commit(machine, op, ups)
}

// EditNote replaces a note's file with data, unless it changed since base or the new file does not parse.
func (s *Store) EditNote(machine, ref, base string, data []byte, checks Checks) (string, error) {
	b, err := s.Brain()
	if err != nil {
		return "", err
	}
	p, ok := b.Resolve(ref)
	if !ok {
		return "", refuse(ErrNoNote, "There is no note %q in the brain.", ref)
	}
	n, err := note.Parse(data)
	if err != nil {
		return "", refuse(ErrInvalid, "The note did not save: %v. Fix the frontmatter and save again.", err)
	}
	before, err := note.Parse(b.Files[p].Content)
	if err != nil {
		before = &note.Note{Body: string(b.Files[p].Content)}
	}
	if err := b.guard(checks, n, p, before); err != nil {
		return "", err
	}
	return Sha(data), s.commit(machine, "", []Upload{upload(p, base, data)})
}

// SetDescription changes a note's description, and its line in MEMORY.md when it has one.
func (s *Store) SetDescription(machine, ref, base, description string) (string, error) {
	description = strings.Join(strings.Fields(description), " ")
	if description == "" {
		return "", refuse(ErrInvalid, "A note needs a one-line description.")
	}
	b, err := s.Brain()
	if err != nil {
		return "", err
	}
	f, n, err := b.Note(ref)
	if err != nil {
		return "", err
	}
	n.Description = description
	data, err := n.Marshal()
	if err != nil {
		return "", err
	}
	ups := []Upload{upload(f.Path, base, data)}
	file := path.Base(f.Path)
	if up, ok := b.index(path.Dir(f.Path), func(s string) string {
		if !strings.Contains(s, "("+file+")") {
			return s
		}
		return brain.WithIndexLine(s, file, file, n)
	}); ok {
		ups = append(ups, up)
	}
	return Sha(data), s.commit(machine, NewOp("edit"), ups)
}

// RenameNote gives a note a new name and file name, rewriting its MEMORY.md line and the links to it in other notes.
func (s *Store) RenameNote(machine, ref, name string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || strings.ContainsAny(name, "[]|#") {
		return "", "", refuse(ErrInvalid, "A note needs a name, without [ ] | or #, which links use.")
	}
	b, err := s.Brain()
	if err != nil {
		return "", "", err
	}
	f, n, err := b.Note(ref)
	if err != nil {
		return "", "", err
	}
	if name == n.Name {
		return f.Path, "", nil
	}
	stem, err := brain.Stem(name)
	if err != nil {
		return "", "", refuse(ErrInvalid, "The name needs at least one letter or digit to make a file name from.")
	}
	folder, file := path.Dir(f.Path), path.Base(f.Path)
	target := folder + "/" + stem + ".md"
	if _, taken := b.Files[target]; taken && target != f.Path {
		return "", "", refuse(ErrTaken, "There is already a note at %s. Pick another name.", target)
	}
	oldName, oldStem := n.Name, strings.TrimSuffix(file, ".md")
	relink := func(text string) string {
		return note.WikiLinkRE.ReplaceAllStringFunc(text, func(m string) string {
			at := note.WikiLinkRE.FindStringSubmatchIndex(m)
			linked := strings.TrimSpace(m[at[2]:at[3]])
			if p, ok := b.Resolve(linked); !ok || p != f.Path {
				return m
			}
			to := name
			if linked == oldStem && linked != oldName {
				to = stem
			}
			return m[:at[2]] + to + m[at[3]:]
		})
	}
	n.Name, n.Description, n.Body = name, relink(n.Description), relink(n.Body)
	data, err := n.Marshal()
	if err != nil {
		return "", "", err
	}
	ups := []Upload{upload(target, "", data)}
	if target == f.Path {
		ups[0].Base = f.Sha
	} else {
		ups = append(ups, upload(f.Path, f.Sha, nil))
	}
	if up, ok := b.index(folder, func(s string) string { return brain.WithIndexLine(s, file, stem+".md", n) }); ok {
		ups = append(ups, up)
	}
	for _, p := range slices.Sorted(maps.Keys(b.Files)) {
		o := b.Files[p]
		if _, isNote := NoteProject(p); !isNote || p == f.Path {
			continue
		}
		if text := relink(string(o.Content)); text != string(o.Content) {
			ups = append(ups, upload(p, o.Sha, []byte(text)))
		}
	}
	op := NewOp("rename")
	return target, op, s.commit(machine, op, ups)
}

// MoveNote puts a note on another shelf, with that shelf's scope, and moves its MEMORY.md line along.
func (s *Store) MoveNote(machine, ref, to string) (string, string, error) {
	folder, scope, err := shelf(to)
	if err != nil {
		return "", "", err
	}
	b, err := s.Brain()
	if err != nil {
		return "", "", err
	}
	f, n, err := b.Note(ref)
	if err != nil {
		return "", "", err
	}
	from, file := path.Dir(f.Path), path.Base(f.Path)
	target := folder + "/" + file
	if folder == from {
		return "", "", refuse(ErrInvalid, "%s is already on that shelf.", f.Path)
	}
	if _, taken := b.Files[target]; taken {
		return "", "", refuse(ErrTaken, "There is already a note at %s. Rename one of them first.", target)
	}
	n.Scope = scope
	data, err := n.Marshal()
	if err != nil {
		return "", "", err
	}
	ups := []Upload{upload(target, "", data), upload(f.Path, f.Sha, nil)}
	if up, ok := b.index(from, func(s string) string { return brain.WithoutIndexEntry(s, file) }); ok {
		ups = append(ups, up)
	}
	if up, ok := b.index(folder, func(s string) string { return brain.WithIndexEntry(s, file, n) }); ok {
		ups = append(ups, up)
	}
	op := NewOp("move")
	return target, op, s.commit(machine, op, ups)
}

// DeleteNote removes a note and its MEMORY.md line; both stay in the history.
func (s *Store) DeleteNote(machine, ref string) (string, error) {
	b, err := s.Brain()
	if err != nil {
		return "", err
	}
	p, ok := b.Resolve(ref)
	if !ok {
		return "", refuse(ErrNoNote, "There is no note %q in the brain.", ref)
	}
	f := b.Files[p]
	ups := []Upload{upload(p, f.Sha, nil)}
	file := path.Base(p)
	if up, ok := b.index(path.Dir(p), func(s string) string { return brain.WithoutIndexEntry(s, file) }); ok {
		ups = append(ups, up)
	}
	op := NewOp("delete")
	return op, s.commit(machine, op, ups)
}

// VerifyNote adds sources to a note and marks it verified today, unless webhooks left open tasks on it.
func (s *Store) VerifyNote(machine, ref string, sources []string) (string, error) {
	b, err := s.Brain()
	if err != nil {
		return "", err
	}
	f, n, err := b.Note(ref)
	if err != nil {
		return "", err
	}
	if bad := slices.IndexFunc(sources, func(s string) bool { return !check.ValidSource(s) }); bad >= 0 {
		return "", refuse(ErrInvalid, "Source %q not understood: use linear:<ID>, file:<repo>/<path>, commit:<repo>@<sha>, note:<name> or a URL.", sources[bad])
	}
	project, _ := NoteProject(f.Path)
	tasks, err := s.Tasks(project)
	if err != nil {
		return "", err
	}
	var open []string
	for _, t := range tasks {
		if t.Note == f.Path {
			open = append(open, t.Ref+": "+t.Message)
		}
	}
	if len(open) > 0 {
		return "", refuse(ErrInvalid, "%s still has open tasks, so it was not verified: %s. Fix them first.", f.Path, strings.Join(open, "; "))
	}
	for _, src := range sources {
		if !slices.Contains(n.Sources, src) {
			n.Sources = append(n.Sources, src)
		}
	}
	n.Verified, _ = time.Parse(time.DateOnly, time.Now().Format(time.DateOnly))
	data, err := n.Marshal()
	if err != nil {
		return "", err
	}
	return Sha(data), s.commit(machine, "", []Upload{upload(f.Path, f.Sha, data)})
}
