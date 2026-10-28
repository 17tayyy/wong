package dashboard

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	brainfs "github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/note"
	"github.com/17tayyy/wong/internal/remote"
)

const changedMsg = "This note changed since you opened it, so your text was not saved."

var opRE = regexp.MustCompile(`^[a-z]+:[0-9a-f]{12}$`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// entryOf loads the brain and the note a request names, answering 404 when there is none.
func (d *Dashboard) entryOf(w http.ResponseWriter, r *http.Request) (*brain, *entry, bool) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return nil, nil, false
	}
	e, ok := b.byPath[r.PathValue("path")]
	if !ok {
		jsonError(w, http.StatusNotFound, "There is no note at "+r.PathValue("path")+" any more. Reload the page.")
		return nil, nil, false
	}
	return b, e, true
}

// parsed loads the note a request names with its frontmatter, answering 422 when it does not parse.
func (d *Dashboard) parsed(w http.ResponseWriter, r *http.Request) (*brain, *entry, *note.Note, bool) {
	b, e, ok := d.entryOf(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	n, err := note.Parse(e.Data)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, "The frontmatter of this note does not parse ("+err.Error()+"). Fix it in the Markdown view first.")
		return nil, nil, nil, false
	}
	return b, e, n, true
}

// apply stores the uploads as one operation and answers 409 when any file changed meanwhile.
func (d *Dashboard) apply(w http.ResponseWriter, machine, op string, ups []remote.Upload) bool {
	conflicts, err := d.store.ApplyOp(dashboardPrefix+machine, op, ups)
	if err != nil {
		d.fail(w, err)
		return false
	}
	if len(conflicts) > 0 {
		jsonError(w, http.StatusConflict, conflicts[0].Path+" changed meanwhile, so nothing was done. Reload the page and try again.")
		return false
	}
	return true
}

func upload(path, base string, data []byte) remote.Upload {
	up := remote.Upload{Path: path, Base: base, Modified: now()}
	if data != nil {
		up.Sha, up.Content = remote.Sha(data), data
	}
	return up
}

// indexUpload returns the change edit makes to a folder's MEMORY.md, or false when it makes none.
func indexUpload(b *brain, folder string, edit func(string) string) (remote.Upload, bool) {
	cur := b.files[folder+"/MEMORY.md"]
	text := edit(string(cur.Content))
	if text == string(cur.Content) {
		return remote.Upload{}, false
	}
	return upload(folder+"/MEMORY.md", cur.Sha, []byte(text)), true
}

// saveBlock puts the text sent in place of lines from up to to of a note's body, or after it when from is -1.
func (d *Dashboard) saveBlock(w http.ResponseWriter, r *http.Request, machine string) {
	b, e, n, ok := d.parsed(w, r)
	if !ok {
		return
	}
	from, err1 := strconv.Atoi(r.FormValue("from"))
	to, err2 := strconv.Atoi(r.FormValue("to"))
	body, fits := spliceBody(n.Body, from, to, r.FormValue("text"))
	if err1 != nil || err2 != nil || r.FormValue("base") != e.Sha || !fits {
		jsonError(w, http.StatusConflict, changedMsg)
		return
	}
	data := append([]byte(nil), e.Data[:len(e.Data)-len(n.Body)]...)
	data = append(data, body...)
	if !d.apply(w, machine, "", []remote.Upload{upload(e.Path, e.Sha, data)}) {
		return
	}
	blocks, err := renderBlocks(body, b.resolve)
	if err != nil {
		d.fail(w, err)
		return
	}
	var html bytes.Buffer
	if err := d.pages["note"].ExecuteTemplate(&html, "blocks", blocks); err != nil {
		d.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sha": remote.Sha(data), "html": html.String()})
}

// saveDescription changes a note's description, and its line in MEMORY.md when it has one.
func (d *Dashboard) saveDescription(w http.ResponseWriter, r *http.Request, machine string) {
	b, e, n, ok := d.parsed(w, r)
	if !ok {
		return
	}
	desc := strings.Join(strings.Fields(r.FormValue("description")), " ")
	if desc == "" {
		jsonError(w, http.StatusUnprocessableEntity, "A note needs a one-line description.")
		return
	}
	if r.FormValue("base") != e.Sha {
		jsonError(w, http.StatusConflict, changedMsg)
		return
	}
	n.Description = desc
	data, err := n.Marshal()
	if err != nil {
		d.fail(w, err)
		return
	}
	ups := []remote.Upload{upload(e.Path, e.Sha, data)}
	file := path.Base(e.Path)
	if up, ok := indexUpload(b, path.Dir(e.Path), func(s string) string {
		if !strings.Contains(s, "("+file+")") {
			return s
		}
		return brainfs.WithIndexLine(s, file, file, n)
	}); ok {
		ups = append(ups, up)
	}
	if d.apply(w, machine, remote.NewOp("edit"), ups) {
		writeJSON(w, http.StatusOK, map[string]string{"sha": remote.Sha(data)})
	}
}

// rename gives a note a new name and file name, and updates its MEMORY.md line and the links to it in other notes.
func (d *Dashboard) rename(w http.ResponseWriter, r *http.Request, machine string) {
	b, e, n, ok := d.parsed(w, r)
	if !ok {
		return
	}
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	if name == "" || strings.ContainsAny(name, "[]|#") {
		jsonError(w, http.StatusUnprocessableEntity, "A note needs a name, without [ ] | or #, which links use.")
		return
	}
	if name == n.Name {
		writeJSON(w, http.StatusOK, map[string]string{"path": e.Path, "url": notePath(e.Path)})
		return
	}
	stem, err := brainfs.Stem(name)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, "The name needs at least one letter or digit to make a file name from.")
		return
	}
	folder, file := path.Dir(e.Path), path.Base(e.Path)
	target := folder + "/" + stem + ".md"
	if _, taken := b.files[target]; taken && target != e.Path {
		jsonError(w, http.StatusConflict, "There is already a note at "+target+". Pick another name.")
		return
	}
	oldName, oldStem := n.Name, strings.TrimSuffix(file, ".md")
	relink := func(text string) string {
		return wikiRE.ReplaceAllStringFunc(text, func(m string) string {
			at := wikiRE.FindStringSubmatchIndex(m)
			linked := strings.TrimSpace(m[at[2]:at[3]])
			if p, ok := b.resolve(linked); !ok || p != e.Path {
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
		d.fail(w, err)
		return
	}
	ups := []remote.Upload{upload(target, "", data)}
	if target == e.Path {
		ups[0].Base = e.Sha
	} else {
		ups = append(ups, upload(e.Path, e.Sha, nil))
	}
	if up, ok := indexUpload(b, folder, func(s string) string { return brainfs.WithIndexLine(s, file, stem+".md", n) }); ok {
		ups = append(ups, up)
	}
	for p, o := range b.byPath {
		if text := relink(string(o.Data)); p != e.Path && text != string(o.Data) {
			ups = append(ups, upload(p, o.Sha, []byte(text)))
		}
	}
	op := remote.NewOp("rename")
	if d.apply(w, machine, op, ups) {
		writeJSON(w, http.StatusOK, map[string]string{"path": target, "url": notePath(target), "op": op})
	}
}

// move puts a note on another shelf, with the scope of that shelf, and moves its MEMORY.md line along.
func (d *Dashboard) move(w http.ResponseWriter, r *http.Request, machine string) {
	b, e, n, ok := d.parsed(w, r)
	if !ok {
		return
	}
	folder, scope, err := brainFolder(r.FormValue("to"))
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, "Pick me or one of the projects.")
		return
	}
	from, file := path.Dir(e.Path), path.Base(e.Path)
	target := folder + "/" + file
	if folder == from {
		jsonError(w, http.StatusUnprocessableEntity, "The note is already on that shelf.")
		return
	}
	if _, taken := b.files[target]; taken {
		jsonError(w, http.StatusConflict, "There is already a note at "+target+". Rename one of them first.")
		return
	}
	n.Scope = scope
	data, err := n.Marshal()
	if err != nil {
		d.fail(w, err)
		return
	}
	ups := []remote.Upload{upload(target, "", data), upload(e.Path, e.Sha, nil)}
	if up, ok := indexUpload(b, from, func(s string) string { return brainfs.WithoutIndexEntry(s, file) }); ok {
		ups = append(ups, up)
	}
	if up, ok := indexUpload(b, folder, func(s string) string { return brainfs.WithIndexEntry(s, file, n) }); ok {
		ups = append(ups, up)
	}
	op := remote.NewOp("move")
	if d.apply(w, machine, op, ups) {
		writeJSON(w, http.StatusOK, map[string]string{"path": target, "url": notePath(target), "op": op})
	}
}

// deleteNote removes a note and its MEMORY.md line; both stay in the history.
func (d *Dashboard) deleteNote(w http.ResponseWriter, r *http.Request, machine string) {
	b, e, ok := d.entryOf(w, r)
	if !ok {
		return
	}
	ups := []remote.Upload{upload(e.Path, e.Sha, nil)}
	file := path.Base(e.Path)
	if up, ok := indexUpload(b, path.Dir(e.Path), func(s string) string { return brainfs.WithoutIndexEntry(s, file) }); ok {
		ups = append(ups, up)
	}
	op := remote.NewOp("delete")
	if d.apply(w, machine, op, ups) {
		writeJSON(w, http.StatusOK, map[string]string{"op": op, "url": "/"})
	}
}

// undoOp reverts a whole operation and goes back to the page it was asked from, or to a note it brought back.
func (d *Dashboard) undoOp(w http.ResponseWriter, r *http.Request, machine string) {
	op := r.PathValue("op")
	if !opRE.MatchString(op) {
		http.NotFound(w, r)
		return
	}
	vs, err := d.store.Op(op)
	if err != nil {
		d.fail(w, err)
		return
	}
	if len(vs) == 0 {
		http.NotFound(w, r)
		return
	}
	_, err = d.store.UndoOp(op, dashboardPrefix+machine)
	if errors.Is(err, remote.ErrNotLatest) || errors.Is(err, sql.ErrNoRows) {
		if wantsJSON(r) {
			jsonError(w, http.StatusConflict, "A later change replaced part of this, so it was not undone. Restore the versions you want from the history.")
			return
		}
		http.Redirect(w, r, "/changes/"+strconv.FormatInt(vs[0].Seq, 10)+"?error=later", http.StatusSeeOther)
		return
	}
	if err != nil {
		d.fail(w, err)
		return
	}
	back := "/"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && strings.HasPrefix(ref.Path, "/") {
		back = ref.EscapedPath()
		if p, isNote := strings.CutPrefix(ref.Path, "/notes/"); isNote && !d.exists(p) {
			back = "/"
			for _, v := range vs {
				if _, isNote := remote.NoteProject(v.Path); isNote && d.exists(v.Path) {
					back = notePath(v.Path)
					break
				}
			}
		}
	}
	back += "?done=reverted"
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]string{"url": back})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (d *Dashboard) exists(p string) bool {
	hist, err := d.store.History(p)
	return err == nil && len(hist) > 0 && !hist[0].Deleted
}
