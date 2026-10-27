package dashboard

import (
	"database/sql"
	"encoding/json"
	"errors"
	"html/template"
	"maps"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	brainfs "github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/remote"
)

type page struct {
	Title    string
	Nav      string
	Machine  string
	Server   string
	Edition  *change
	Next     string
	Error    string
	Notice   string
	Masthead masthead

	Filter  string
	Query   string
	Filters []filter
	Groups  []shelf
	Shelves []shelf
	Open    string
	Slips   []slip
	Changes []change
	More    int

	Entry    *entry
	Links    []*entry
	Mentions []*entry
	Path     string
	Body     template.HTML
	Raw      bool
	Content  string
	Base     string
	Versions []change

	Change *changeView
	Older  int64
	Skills []skillRow

	Form    newNote
	Folders []string
	Kinds   []note.Kind
}

// newNote is what the new-note form holds.
type newNote struct {
	Folder, Name, Description, Kind, Body string
}

type masthead struct {
	Notes       int
	Projects    int
	Corrections int
	Machines    []machineSeen
}

type machineSeen struct {
	Name string
	Seen time.Time
}

type filter struct {
	ID, Label string
	Count     int
	Blue      bool
}

// slip is one open correction on a note.
type slip struct {
	Path   string
	Name   string
	Folder string
	issue
}

// change is one entry of the brain's history, with what it did to the file.
type change struct {
	Seq     int64
	Path    string
	Name    string
	Folder  string
	Machine string
	When    time.Time
	Action  string
	Plus    int
	Minus   int
	Latest  bool
}

type changeView struct {
	change
	Rows     []row
	Lines    []diffLine
	Previous int64
	Deleted  bool
}

// row is one line of a side-by-side comparison: the version before on the left, the change on the right.
type row struct {
	Left, Right       string
	LeftOp, RightOp   string
	LeftNum, RightNum int
}

type skillRow struct {
	Name, Source, Path string
}

func (d *Dashboard) base(r *http.Request, nav, machine string, b *brain) (page, error) {
	p := page{Nav: nav, Machine: machine, Shelves: b.Shelves, Server: r.Host}
	tokens, err := d.store.Tokens()
	if err != nil {
		return p, err
	}
	for _, t := range tokens {
		seen, _ := time.Parse(time.RFC3339, t.LastUsed)
		p.Masthead.Machines = append(p.Masthead.Machines, machineSeen{t.Name, seen})
	}
	for _, s := range b.Shelves {
		p.Masthead.Notes += len(s.Entries)
		if s.ID != "me" {
			p.Masthead.Projects++
		}
		for _, e := range s.Entries {
			if len(e.Issues) > 0 {
				p.Masthead.Corrections++
			}
		}
	}
	return p, nil
}

func (b *brain) slips() []slip {
	var out []slip
	for _, s := range b.Shelves {
		for _, e := range s.Entries {
			for _, i := range e.Issues {
				out = append(out, slip{e.Path, e.Name, s.ID, i})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b slip) int {
		if (a.Severity == check.Stale) != (b.Severity == check.Stale) {
			if a.Severity == check.Stale {
				return -1
			}
			return 1
		}
		return 0
	})
	return out
}

func (d *Dashboard) library(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "library", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "Library"
	p.Filter, p.Query = r.URL.Query().Get("show"), strings.TrimSpace(r.URL.Query().Get("q"))
	recent, err := d.recent(7 * 24 * time.Hour)
	if err != nil {
		d.fail(w, err)
		return
	}
	for i := range b.Shelves {
		for j := range b.Shelves[i].Entries {
			if c, ok := recent[b.Shelves[i].Entries[j].Path]; ok {
				b.Shelves[i].Entries[j].Changed = &c
			}
		}
	}
	keep := map[string]func(entry) bool{
		"pending": func(e entry) bool { return len(e.Issues) > 0 },
		"old":     func(e entry) bool { return time.Since(e.AsOf()) > 60*24*time.Hour },
		"week":    func(e entry) bool { return e.Changed != nil },
	}
	if keep[p.Filter] == nil {
		p.Filter = ""
	}
	counts := map[string]int{}
	var shelves []shelf
	for _, s := range b.Shelves {
		kept := shelf{ID: s.ID}
		for _, e := range s.Entries {
			for id, f := range keep {
				if f(e) {
					counts[id]++
				}
			}
			if p.Filter != "" && !keep[p.Filter](e) || p.Query != "" && !matches(e, p.Query) {
				continue
			}
			kept.Entries = append(kept.Entries, e)
		}
		if len(kept.Entries) > 0 {
			shelves = append(shelves, kept)
		}
	}
	p.Filters = []filter{
		{"", "All notes", p.Masthead.Notes, false},
		{"pending", "Needs correction", counts["pending"], true},
		{"old", "Over 60 days", counts["old"], false},
		{"week", "Changed this week", counts["week"], false},
	}
	p.Groups = shelves
	p.Slips = b.slips()
	if len(p.Slips) > 6 {
		p.More, p.Slips = len(p.Slips)-6, p.Slips[:6]
	}
	if p.Changes, err = d.changes(b, 6, 0); err != nil {
		d.fail(w, err)
		return
	}
	if len(p.Changes) > 0 {
		p.Edition = &p.Changes[0]
	}
	d.render(w, "library", p)
}

func matches(e entry, q string) bool {
	q = strings.ToLower(q)
	for _, s := range []string{e.Name, e.Description, e.Path, e.Note.Body} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// recent returns the latest change of each file changed within the window. Files created in bulk, more than
// bulkSync by one machine within a minute, are a first sync rather than news, and are left out.
func (d *Dashboard) recent(window time.Duration) (map[string]change, error) {
	var versions []remote.Version
	var before int64
	for done := false; !done; {
		log, err := d.store.Log(500, before)
		if err != nil {
			return nil, err
		}
		if len(log) == 0 {
			break
		}
		for _, v := range log {
			if time.Since(v.Created) > window {
				done = true
				break
			}
			versions = append(versions, v)
		}
		before = log[len(log)-1].Seq
	}
	out := map[string]change{}
	created := map[string]int{}
	actions := make([]string, len(versions))
	for i, v := range versions {
		actions[i] = "edited"
		if prev, _, ok, err := d.store.Previous(v); err != nil {
			return nil, err
		} else if !ok || prev.Deleted {
			actions[i] = "new"
			created[v.Machine+v.Created.Truncate(time.Minute).String()]++
		}
	}
	for i, v := range versions {
		if _, seen := out[v.Path]; seen || v.Deleted {
			if !seen {
				out[v.Path] = change{}
			}
			continue
		}
		if actions[i] == "new" && created[v.Machine+v.Created.Truncate(time.Minute).String()] > bulkSync {
			out[v.Path] = change{}
			continue
		}
		out[v.Path] = change{Seq: v.Seq, Path: v.Path, Machine: v.Machine, When: v.Created, Latest: true, Action: actions[i]}
	}
	for path, c := range out {
		if c.Seq == 0 {
			delete(out, path)
		}
	}
	return out, nil
}

const bulkSync = 20

// changes describes up to limit changes of the brain's history, older than before when it is not 0.
func (d *Dashboard) changes(b *brain, limit int, before int64) ([]change, error) {
	log, err := d.store.Log(limit, before)
	if err != nil {
		return nil, err
	}
	out := make([]change, 0, len(log))
	for _, v := range log {
		c, err := d.describe(b, v)
		if err != nil {
			return nil, err
		}
		out = append(out, c.change)
	}
	return out, nil
}

func (d *Dashboard) describe(b *brain, v remote.Version) (changeView, error) {
	c := changeView{change: change{Seq: v.Seq, Path: v.Path, Name: strings.TrimSuffix(path.Base(v.Path), ".md"),
		Folder: folderOf(v.Path), Machine: v.Machine, When: v.Created}, Deleted: v.Deleted}
	if e, ok := b.byPath[v.Path]; ok {
		c.Name = e.Name
		c.Latest = e.Seq == v.Seq
	} else if hist, err := d.store.History(v.Path); err == nil && len(hist) > 0 {
		c.Latest = hist[0].Seq == v.Seq
	}
	_, after, err := d.store.Version(v.Seq)
	if err != nil {
		return c, err
	}
	prev, before, ok, err := d.store.Previous(v)
	if err != nil {
		return c, err
	}
	if _, isNote := b.byPath[v.Path]; !isNote && strings.HasSuffix(v.Path, ".md") {
		for _, data := range [][]byte{after, before} {
			if n, err := note.Parse(data); err == nil && n.Name != "" {
				c.Name = n.Name
				break
			}
		}
	}
	switch {
	case v.Deleted:
		c.Action = "deleted"
	case !ok || prev.Deleted:
		c.Action = "created"
	default:
		c.Action = "edited"
	}
	if ok {
		c.Previous = prev.Seq
	}
	if !ok || prev.Deleted {
		before = nil
	}
	if v.Deleted {
		after = nil
	}
	lines := diffLines(string(before), string(after))
	c.Plus, c.Minus = diffStat(lines)
	c.Rows, c.Lines = sideBySide(lines), lines
	return c, nil
}

// sideBySide lines up a diff in two columns, pairing each removed line with the line that replaced it.
func sideBySide(lines []diffLine) []row {
	var rows []row
	left, right := 0, 0
	for i := 0; i < len(lines); {
		if lines[i].Op == same {
			left, right = left+1, right+1
			rows = append(rows, row{lines[i].Text, lines[i].Text, "same", "same", left, right})
			i++
			continue
		}
		var del, add []string
		for ; i < len(lines) && lines[i].Op != same; i++ {
			if lines[i].Op == removed {
				del = append(del, lines[i].Text)
			} else {
				add = append(add, lines[i].Text)
			}
		}
		for j := range max(len(del), len(add)) {
			var r row
			if j < len(del) {
				left++
				r.Left, r.LeftOp, r.LeftNum = del[j], "removed", left
			}
			if j < len(add) {
				right++
				r.Right, r.RightOp, r.RightNum = add[j], "added", right
			}
			rows = append(rows, r)
		}
	}
	return rows
}

func (d *Dashboard) note(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "library", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Path = r.PathValue("path")
	p.Raw = r.URL.Query().Get("view") == "markdown"
	hist, err := d.store.History(p.Path)
	if err != nil {
		d.fail(w, err)
		return
	}
	if len(hist) == 0 {
		http.NotFound(w, r)
		return
	}
	p.Open = folderOf(p.Path)
	if e, ok := b.byPath[p.Path]; ok {
		p.Entry = e
		p.Title = e.Name
		for _, l := range e.Links {
			p.Links = append(p.Links, b.byPath[l])
		}
		for _, l := range e.Backlinks {
			p.Mentions = append(p.Mentions, b.byPath[l])
		}
		p.Content = string(e.Data)
		if p.Body, err = renderMarkdown(e.Note.Body, b.resolve); err != nil {
			d.fail(w, err)
			return
		}
	} else {
		p.Title = path.Base(p.Path)
		p.Raw = true
		if !hist[0].Deleted {
			_, data, err := d.store.Version(hist[0].Seq)
			if err != nil {
				d.fail(w, err)
				return
			}
			p.Content = string(data)
		}
	}
	for _, v := range hist {
		c, err := d.describe(b, v)
		if err != nil {
			d.fail(w, err)
			return
		}
		p.Versions = append(p.Versions, c.change)
	}
	p.Notice = notice(r)
	d.render(w, "note", p)
}

func (d *Dashboard) edit(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "library", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Path = r.PathValue("path")
	e, ok := b.byPath[p.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	p.Entry, p.Title, p.Content, p.Base, p.Open = e, "Edit "+e.Name, string(e.Data), e.Sha, folderOf(p.Path)
	d.render(w, "edit", p)
}

func (d *Dashboard) save(w http.ResponseWriter, r *http.Request, machine string) {
	target := r.PathValue("path")
	content := strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	retry := func(msg string) {
		b, err := loadBrain(d.store)
		if err != nil {
			d.fail(w, err)
			return
		}
		p, err := d.base(r, "library", machine, b)
		if err != nil {
			d.fail(w, err)
			return
		}
		p.Path, p.Content, p.Base, p.Error = target, content, r.FormValue("base"), msg
		if e, ok := b.byPath[target]; ok {
			p.Entry, p.Title = e, "Edit "+e.Name
		}
		w.WriteHeader(http.StatusConflict)
		d.render(w, "edit", p)
	}
	if _, ok := remote.NoteProject(target); ok {
		if _, err := note.Parse([]byte(content)); err != nil {
			retry("The note did not save: " + err.Error() + ". Fix the frontmatter and save again.")
			return
		}
	}
	up := remote.Upload{Path: target, Base: r.FormValue("base"), Sha: remote.Sha([]byte(content)), Content: []byte(content),
		Modified: time.Now().UTC().Format(time.RFC3339Nano)}
	_, conflicts, err := d.store.Apply(dashboardPrefix+machine, []remote.Upload{up})
	if errors.Is(err, remote.ErrBadPush) {
		retry("The note did not save: " + err.Error() + ".")
		return
	}
	if err != nil {
		d.fail(w, err)
		return
	}
	if len(conflicts) > 0 {
		retry("This note changed since you opened it. Your text is below: copy what you need, open the note again and redo the edit there.")
		return
	}
	http.Redirect(w, r, notePath(target)+"?done=saved", http.StatusSeeOther)
}

func (d *Dashboard) corrections(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "corrections", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "Corrections"
	p.Slips = b.slips()
	d.render(w, "corrections", p)
}

const historyPage = 50

func (d *Dashboard) history(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "history", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "History"
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if p.Changes, err = d.changes(b, historyPage, before); err != nil {
		d.fail(w, err)
		return
	}
	if len(p.Changes) == historyPage {
		p.Older = p.Changes[len(p.Changes)-1].Seq
	}
	p.Notice = notice(r)
	d.render(w, "history", p)
}

func (d *Dashboard) change(w http.ResponseWriter, r *http.Request, machine string) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "history", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	v, _, err := d.store.Version(seq)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		d.fail(w, err)
		return
	}
	c, err := d.describe(b, v)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Change, p.Title = &c, c.Name
	if e, ok := b.byPath[v.Path]; ok {
		p.Base = e.Sha
	}
	switch r.URL.Query().Get("error") {
	case "later":
		p.Error = "A later change to this file exists, so undoing this one would throw that away too. Restore this version instead if that is what you want."
	case "changed":
		p.Error = "The file changed while you were looking at it. Nothing was restored; look again and retry."
	}
	d.render(w, "change", p)
}

func (d *Dashboard) undo(w http.ResponseWriter, r *http.Request, machine string) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	err = d.store.Undo(seq, dashboardPrefix+machine)
	if errors.Is(err, remote.ErrNotLatest) {
		http.Redirect(w, r, "/changes/"+strconv.FormatInt(seq, 10)+"?error=later", http.StatusSeeOther)
		return
	}
	if err != nil {
		d.fail(w, err)
		return
	}
	http.Redirect(w, r, "/history?done=undone", http.StatusSeeOther)
}

func (d *Dashboard) restore(w http.ResponseWriter, r *http.Request, machine string) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, _, err := d.store.Version(seq)
	if err != nil {
		d.fail(w, err)
		return
	}
	err = d.store.Restore(seq, r.FormValue("base"), dashboardPrefix+machine)
	if errors.Is(err, remote.ErrNotLatest) {
		http.Redirect(w, r, "/changes/"+strconv.FormatInt(seq, 10)+"?error=changed", http.StatusSeeOther)
		return
	}
	if err != nil {
		d.fail(w, err)
		return
	}
	if v.Deleted {
		http.Redirect(w, r, "/history?done=restored", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, notePath(v.Path)+"?done=restored", http.StatusSeeOther)
}

func (d *Dashboard) skills(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "skills", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "Skills"
	files, err := d.store.Files()
	if err != nil {
		d.fail(w, err)
		return
	}
	seen := map[string]bool{}
	for _, f := range files {
		if f.Path != "skills/skills-lock.json" {
			continue
		}
		var lock struct {
			Skills map[string]struct {
				Source     string `json:"source"`
				SourceType string `json:"sourceType"`
			} `json:"skills"`
		}
		if json.Unmarshal(f.Content, &lock) == nil {
			for _, name := range slices.Sorted(maps.Keys(lock.Skills)) {
				s := lock.Skills[name]
				p.Skills = append(p.Skills, skillRow{name, s.Source + " (" + s.SourceType + ")", ""})
				seen[name] = true
			}
		}
	}
	for _, dir := range b.Skills {
		name := path.Base(dir)
		where := "copied into the brain"
		if rest, ok := strings.CutPrefix(path.Dir(dir), "skills/projects/"); ok {
			where = "copied from " + rest
		}
		if !seen[name] || strings.HasPrefix(dir, "skills/projects/") {
			p.Skills = append(p.Skills, skillRow{name, where, dir + "/SKILL.md"})
		}
	}
	d.render(w, "skills", p)
}

func notice(r *http.Request) string {
	switch r.URL.Query().Get("done") {
	case "saved":
		return "Saved. The other machines get it on their next pull."
	case "undone":
		return "Undone. The change back is the newest entry below; it can be undone too."
	case "restored":
		return "Restored. The restore is a new change and can be undone too."
	case "created":
		return "Created, and added to its folder's MEMORY.md. The machines get it on their next pull."
	}
	return ""
}

func (d *Dashboard) newForm(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "library", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "New note"
	p.Form = newNote{Folder: r.URL.Query().Get("in"), Kind: string(note.Project)}
	if p.Form.Folder == "me" || p.Form.Folder == "" {
		p.Form.Folder, p.Form.Kind = "me", string(note.Preference)
	}
	d.newPage(w, &p, b)
}

func (d *Dashboard) newPage(w http.ResponseWriter, p *page, b *brain) {
	for _, s := range b.Shelves {
		p.Folders = append(p.Folders, s.ID)
	}
	if !slices.Contains(p.Folders, "me") {
		p.Folders = append([]string{"me"}, p.Folders...)
	}
	p.Kinds = []note.Kind{note.Preference, note.Project, note.Decision, note.Reference}
	d.render(w, "new", *p)
}

func (d *Dashboard) create(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "library", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "New note"
	form := newNote{Folder: r.FormValue("folder"), Name: strings.TrimSpace(r.FormValue("name")),
		Description: strings.TrimSpace(r.FormValue("description")), Kind: r.FormValue("kind"),
		Body: strings.ReplaceAll(r.FormValue("body"), "\r\n", "\n")}
	p.Form = form
	retry := func(msg string) {
		p.Error = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		d.newPage(w, &p, b)
	}
	folder, scope, err := brainFolder(form.Folder)
	if err != nil {
		retry("Pick me or one of the projects for the note.")
		return
	}
	kind, err := note.ParseKind(form.Kind)
	if err != nil || form.Name == "" || form.Description == "" {
		retry("A note needs a name, a one-line description and a kind.")
		return
	}
	stem, err := brainfs.Stem(form.Name)
	if err != nil {
		retry("The name needs at least one letter or digit to make a file name from.")
		return
	}
	body := form.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	n := note.New(form.Name, form.Description, kind, scope, body)
	data, err := n.Marshal()
	if err != nil {
		d.fail(w, err)
		return
	}
	target := folder + "/" + stem + ".md"
	if _, exists := b.byPath[target]; exists {
		retry("There is already a note at " + target + ": give this one another name, or edit that one.")
		return
	}
	index, indexSha := "", ""
	files, err := d.store.Files()
	if err != nil {
		d.fail(w, err)
		return
	}
	for _, f := range files {
		if f.Path == folder+"/MEMORY.md" {
			index, indexSha = string(f.Content), f.Sha
		}
	}
	newIndex := brainfs.WithIndexEntry(index, stem+".md", n)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ups := []remote.Upload{
		{Path: target, Sha: remote.Sha(data), Content: data, Modified: now},
		{Path: folder + "/MEMORY.md", Base: indexSha, Sha: remote.Sha([]byte(newIndex)), Content: []byte(newIndex), Modified: now},
	}
	_, conflicts, err := d.store.Apply(dashboardPrefix+machine, ups)
	if err != nil {
		d.fail(w, err)
		return
	}
	if len(conflicts) > 0 {
		retry("The note was not created: " + target + " or its folder's MEMORY.md changed meanwhile. Check the library and try again.")
		return
	}
	http.Redirect(w, r, notePath(target)+"?done=created", http.StatusSeeOther)
}

// brainFolder returns the folder and scope of a note for me or a project id.
func brainFolder(target string) (string, string, error) {
	folder, scope, err := brainfs.Folder(target)
	return strings.ReplaceAll(folder, "\\", "/"), scope, err
}
