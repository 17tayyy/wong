package dashboard

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"net/url"
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
	Title       string
	Nav         string
	Machine     string
	Server      string
	Edition     *change
	Next        string
	Error       string
	Notice      string
	SignInURL   string
	AccountNav  bool
	AccountPage *accountInfo
	Encryption  *encryptionInfo
	Storage     string
	Snack       *snack
	Masthead    masthead

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
	Blocks   []block
	Editable bool
	Raw      bool
	Content  string
	Base     string
	Versions []change

	Change     *changeView
	Older      int64
	Skills     []skillRow
	Hooks      []hookView
	Device     *remote.Device
	Connection *connection
	Taken      bool

	Form    newNote
	Folders []string
	Kinds   []note.Kind

	Graph      string
	GraphNotes int
	GraphLinks int
	LocalGraph string
	TagNames   []string
	KindNames  []string
}

// newNote is what the new-note form holds.
type newNote struct {
	Folder, Name, Description, Kind, Body string
}

// snack is the note shown at the foot of the page after an operation, with the way to undo it.
type snack struct {
	Text, Op, Close string
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
	Op      string
	Also    int
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

var snacks = map[string]string{
	"renamed":    "Renamed to “%[1]s”, with its line in MEMORY.md and the links to it.",
	"moved":      "Moved “%[1]s” to %[2]s, with its scope and MEMORY.md line.",
	"deleted":    "Deleted “%[1]s”. It stays in the history.",
	"archived":   "Archived “%[1]s”. Agents leave it alone until someone asks for it.",
	"unarchived": "Brought “%[1]s” back to %[2]s.",
	"reverted":   "Undone.",
}

func (d *Dashboard) base(r *http.Request, nav, machine string, b *brain) (page, error) {
	p := page{Nav: nav, Machine: machine, Shelves: b.Shelves, Server: r.Host, AccountNav: d.opts.Account != nil && d.account != ""}
	if d.account != "" {
		p.Server = d.account
	}
	if limit := d.store.Limits().Size; limit > 0 {
		size, err := d.store.Size()
		if err != nil {
			return p, err
		}
		p.Storage = "Using " + remote.Bytes(size) + " of " + remote.Bytes(limit)
	}
	q := r.URL.Query()
	if format, ok := snacks[q.Get("done")]; ok {
		p.Snack = &snack{Text: format}
		if op := q.Get("op"); opRE.MatchString(op) {
			vs, err := d.store.Op(op)
			if err != nil {
				return p, err
			}
			if len(vs) > 0 {
				c, err := d.describeOp(b, vs)
				if err != nil {
					return p, err
				}
				p.Snack.Op, p.Snack.Text = op, fmt.Sprintf(format, c.Name, c.Folder)
			}
		}
		q.Del("done")
		q.Del("op")
		p.Snack.Close = (&url.URL{Path: r.URL.Path, RawQuery: q.Encode()}).String()
	}
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
		if p.Filter == "" && p.Query == "" {
			kept.Archive = s.Archive
		}
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
		if len(kept.Entries) > 0 || len(kept.Archive) > 0 {
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
	if p.Changes, _, err = d.changes(b, 1, 0); err != nil {
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

// changes describes up to limit changes of the brain's history, older than before when it is not 0, with the files of
// one operation as a single change; it also returns the oldest sequence number it read.
func (d *Dashboard) changes(b *brain, limit int, before int64) ([]change, int64, error) {
	log, err := d.store.Log(limit, before)
	if err != nil {
		return nil, 0, err
	}
	out := make([]change, 0, len(log))
	seen := map[string]bool{}
	var oldest int64
	for _, v := range log {
		oldest = v.Seq
		if v.Op == "" {
			c, err := d.describe(b, v)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, c.change)
			continue
		}
		if seen[v.Op] {
			continue
		}
		seen[v.Op] = true
		vs, err := d.store.Op(v.Op)
		if err != nil {
			return nil, 0, err
		}
		if before > 0 && vs[len(vs)-1].Seq >= before {
			continue
		}
		c, err := d.describeOp(b, vs)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, oldest, nil
}

var opActions = map[string]string{"create": "created", "edit": "edited", "rename": "renamed", "move": "moved", "delete": "deleted", "undo": "undone",
	"archive": "archived", "unarchive": "brought back"}

// describeOp describes an operation by its note, with how many other files it changed and whether it can still be undone.
func (d *Dashboard) describeOp(b *brain, vs []remote.Version) (change, error) {
	main := vs[0]
	if i := slices.IndexFunc(vs, isNote); i >= 0 {
		main = vs[i]
	}
	if i := slices.IndexFunc(vs, func(v remote.Version) bool { return isNote(v) && !v.Deleted }); i >= 0 {
		main = vs[i]
	}
	c, err := d.describe(b, main)
	if err != nil {
		return change{}, err
	}
	kind, _, _ := strings.Cut(main.Op, ":")
	if a, ok := opActions[kind]; ok {
		c.Action = a
	}
	c.Op, c.Also, c.Latest = main.Op, len(vs)-1, true
	for _, v := range vs {
		if hist, err := d.store.History(v.Path); err != nil || len(hist) == 0 || hist[0].Seq != v.Seq {
			c.Latest = false
		}
	}
	return c.change, nil
}

func isNote(v remote.Version) bool {
	_, ok := remote.NoteProject(v.Path)
	_, archived := remote.ArchivedProject(v.Path)
	return ok || archived
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
		p.Content, p.Editable = string(e.Data), e.Valid
		if len(e.Links)+len(e.Backlinks) > 0 {
			p.LocalGraph = local(b, e).json()
		}
		if p.Blocks, err = renderBlocks(e.Note.Body, b.resolve); err != nil {
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
	http.Redirect(w, r, notePath(r.PathValue("path"))+"?view=markdown", http.StatusSeeOther)
}

func (d *Dashboard) save(w http.ResponseWriter, r *http.Request, machine string) {
	target := r.PathValue("path")
	content := strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	retry := func(msg string) {
		if wantsJSON(r) {
			jsonError(w, http.StatusConflict, msg)
			return
		}
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
	sha, err := d.store.EditNote(dashboardPrefix+machine, target, r.FormValue("base"), []byte(content), remote.Checks{})
	var ne *remote.NoteError
	switch {
	case errors.As(err, &ne) && ne.Kind == remote.ErrChanged && wantsJSON(r):
		jsonError(w, http.StatusConflict, changedMsg)
		return
	case errors.As(err, &ne) && ne.Kind == remote.ErrChanged:
		retry("This note changed since you opened it. Your text is below: copy what you need, open the note again and redo the edit there.")
		return
	case errors.As(err, &ne):
		retry(ne.Msg)
		return
	case err != nil:
		d.fail(w, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]string{"sha": sha})
		return
	}
	http.Redirect(w, r, notePath(target)+"?view=markdown&done=saved", http.StatusSeeOther)
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
	var oldest int64
	if p.Changes, oldest, err = d.changes(b, historyPage, before); err != nil {
		d.fail(w, err)
		return
	}
	if older, err := d.store.Log(1, oldest); err == nil && len(older) > 0 && oldest > 0 {
		p.Older = oldest
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
	if v, _, err := d.store.Version(seq); err == nil && v.Op != "" {
		r.SetPathValue("op", v.Op)
		d.undoOp(w, r, machine)
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
	case "hook-new":
		return "A new secret is ready below. Paste it into each repo's webhook; the old one no longer works."
	case "hook-saved":
		return "Saved. wong takes the webhook from now on."
	case "hook-off":
		return "Turned off. wong refuses the webhook until it has a secret again."
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
	if _, _, err := brainFolder(form.Folder); err != nil {
		retry("Pick me or one of the projects for the note.")
		return
	}
	kind, err := note.ParseKind(form.Kind)
	if err != nil || form.Name == "" || form.Description == "" {
		retry("A note needs a name, a one-line description and a kind.")
		return
	}
	body := form.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	target, _, err := d.store.CreateNote(dashboardPrefix+machine, form.Folder, note.New(form.Name, form.Description, kind, "", body), remote.Checks{})
	var ne *remote.NoteError
	if errors.As(err, &ne) {
		retry(ne.Msg)
		return
	}
	if err != nil {
		d.fail(w, err)
		return
	}
	http.Redirect(w, r, notePath(target)+"?done=created", http.StatusSeeOther)
}

// brainFolder returns the folder and scope of a note for me or a project id.
func brainFolder(target string) (string, string, error) {
	folder, scope, err := brainfs.Folder(target)
	return strings.ReplaceAll(folder, "\\", "/"), scope, err
}
