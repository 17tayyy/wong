package dashboard

import (
	"cmp"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/remote"
)

// entry is one note of the library with what is wrong with it.
type entry struct {
	Path        string
	Name        string
	Description string
	Kind        note.Kind
	Verified    time.Time
	Written     time.Time
	Issues      []issue
	Changed     *change
	Links       []string
	Backlinks   []string
	Note        *note.Note
	Data        []byte
	Sha         string
	Seq         int64
}

// issue is a finding on a note and who raised it: a machine's check or a webhook.
type issue struct {
	check.Finding
	Source string
}

// Stale reports whether a finding says the note contradicts its source.
func (e entry) Stale() bool {
	return slices.ContainsFunc(e.Issues, func(i issue) bool { return i.Severity == check.Stale })
}

// ShowDescription reports whether the description says more than the name.
func (e entry) ShowDescription() bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), ".")) }
	return e.Description != "" && norm(e.Description) != norm(e.Name)
}

// AsOf is when the note was last confirmed: verified, else last written.
func (e entry) AsOf() time.Time {
	if !e.Verified.IsZero() {
		return e.Verified
	}
	return e.Written
}

// shelf is a folder of the library: me or one project.
type shelf struct {
	ID      string
	Entries []entry
}

func (s shelf) Count(stale bool) int {
	n := 0
	for _, e := range s.Entries {
		if len(e.Issues) > 0 && e.Stale() == stale {
			n++
		}
	}
	return n
}

// brain is the server's brain as the dashboard reads it.
type brain struct {
	Shelves []shelf
	byPath  map[string]*entry
	byName  map[string]string
	Skills  []string
	Other   []string
}

func loadBrain(store *remote.Store) (*brain, error) {
	files, err := store.Files()
	if err != nil {
		return nil, err
	}
	findings, err := store.Findings("")
	if err != nil {
		return nil, err
	}
	tasks, err := store.Tasks("")
	if err != nil {
		return nil, err
	}
	issues := map[string][]issue{}
	for _, f := range findings {
		issues[f.Note] = append(issues[f.Note], issue{f, "check"})
	}
	for _, t := range tasks {
		if slices.ContainsFunc(issues[t.Note], func(i issue) bool { return i.Ref == t.Ref }) {
			continue
		}
		source, _, _ := strings.Cut(t.Source, " ")
		f := check.Finding{Note: t.Note, Severity: check.Severity(t.Severity), Ref: t.Ref, Message: t.Message}
		issues[t.Note] = append(issues[t.Note], issue{f, map[string]string{"github": "GitHub", "linear": "Linear"}[source] + " webhook"})
	}
	b := &brain{byPath: map[string]*entry{}, byName: map[string]string{}}
	shelves := map[string]*shelf{}
	for _, f := range files {
		if strings.HasPrefix(f.Path, "skills/") {
			if path.Base(f.Path) == "SKILL.md" {
				b.Skills = append(b.Skills, path.Dir(f.Path))
			}
			continue
		}
		id, ok := remote.NoteProject(f.Path)
		if !ok {
			b.Other = append(b.Other, f.Path)
			continue
		}
		n, err := note.Parse(f.Content)
		if err != nil {
			n = &note.Note{Name: strings.TrimSuffix(path.Base(f.Path), ".md"), Body: string(f.Content)}
		}
		written, _ := time.Parse(time.RFC3339Nano, f.Modified)
		if m := n.Modified(); m.After(written) {
			written = m
		}
		e := entry{Path: f.Path, Name: n.Name, Description: n.Description, Kind: n.Kind, Verified: n.Verified,
			Written: written, Issues: issues[f.Path], Note: n, Data: f.Content, Sha: f.Sha, Seq: f.Seq}
		if shelves[id] == nil {
			shelves[id] = &shelf{ID: id}
		}
		shelves[id].Entries = append(shelves[id].Entries, e)
		b.byName[n.Name] = f.Path
		b.byName[strings.TrimSuffix(path.Base(f.Path), ".md")] = f.Path
	}
	ids := slices.Collect(maps.Keys(shelves))
	slices.SortFunc(ids, func(a, b string) int {
		if a == "me" || b == "me" {
			return cmp.Compare(boolInt(b == "me"), boolInt(a == "me"))
		}
		return strings.Compare(a, b)
	})
	for _, id := range ids {
		s := shelves[id]
		slices.SortFunc(s.Entries, func(a, b entry) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		b.Shelves = append(b.Shelves, *s)
	}
	for i := range b.Shelves {
		for j := range b.Shelves[i].Entries {
			e := &b.Shelves[i].Entries[j]
			b.byPath[e.Path] = e
		}
	}
	for _, e := range b.byPath {
		for _, m := range wikiRE.FindAllStringSubmatch(e.Note.Body, -1) {
			target, ok := b.resolve(strings.TrimSpace(m[1]))
			if !ok || target == e.Path || slices.Contains(e.Links, target) {
				continue
			}
			e.Links = append(e.Links, target)
			b.byPath[target].Backlinks = append(b.byPath[target].Backlinks, e.Path)
		}
	}
	for _, e := range b.byPath {
		slices.Sort(e.Links)
		slices.Sort(e.Backlinks)
	}
	return b, nil
}

func (b *brain) resolve(name string) (string, bool) {
	p, ok := b.byName[name]
	return p, ok
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
