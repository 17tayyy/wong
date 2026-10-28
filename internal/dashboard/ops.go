package dashboard

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

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

// refused answers a note operation the store turned down with its reason, or fails on any other error.
func (d *Dashboard) refused(w http.ResponseWriter, err error) {
	var ne *remote.NoteError
	if !errors.As(err, &ne) {
		d.fail(w, err)
		return
	}
	switch ne.Kind {
	case remote.ErrNoNote:
		jsonError(w, http.StatusNotFound, ne.Msg)
	case remote.ErrChanged:
		jsonError(w, http.StatusConflict, changedMsg)
	case remote.ErrTaken:
		jsonError(w, http.StatusConflict, ne.Msg)
	default:
		jsonError(w, http.StatusUnprocessableEntity, ne.Msg)
	}
}

// saveBlock puts the text sent in place of lines from up to to of a note's body, or after it when from is -1.
func (d *Dashboard) saveBlock(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := d.store.Brain()
	if err != nil {
		d.fail(w, err)
		return
	}
	f, n, err := b.Note(r.PathValue("path"))
	if err != nil {
		d.refused(w, err)
		return
	}
	from, err1 := strconv.Atoi(r.FormValue("from"))
	to, err2 := strconv.Atoi(r.FormValue("to"))
	body, fits := spliceBody(n.Body, from, to, r.FormValue("text"))
	if err1 != nil || err2 != nil || r.FormValue("base") != f.Sha || !fits {
		jsonError(w, http.StatusConflict, changedMsg)
		return
	}
	data := append([]byte(nil), f.Content[:len(f.Content)-len(n.Body)]...)
	data = append(data, body...)
	sha, err := d.store.EditNote(dashboardPrefix+machine, f.Path, f.Sha, data, remote.Checks{})
	if err != nil {
		d.refused(w, err)
		return
	}
	blocks, err := renderBlocks(body, b.Resolve)
	if err != nil {
		d.fail(w, err)
		return
	}
	var html bytes.Buffer
	if err := d.pages["note"].ExecuteTemplate(&html, "blocks", blocks); err != nil {
		d.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sha": sha, "html": html.String()})
}

// saveDescription changes a note's description, and its line in MEMORY.md when it has one.
func (d *Dashboard) saveDescription(w http.ResponseWriter, r *http.Request, machine string) {
	sha, err := d.store.SetDescription(dashboardPrefix+machine, r.PathValue("path"), r.FormValue("base"), r.FormValue("description"))
	if err != nil {
		d.refused(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sha": sha})
}

// rename gives a note a new name and file name, and updates its MEMORY.md line and the links to it in other notes.
func (d *Dashboard) rename(w http.ResponseWriter, r *http.Request, machine string) {
	target, op, err := d.store.RenameNote(dashboardPrefix+machine, r.PathValue("path"), r.FormValue("name"))
	if err != nil {
		d.refused(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": target, "url": notePath(target), "op": op})
}

// move puts a note on another shelf, with the scope of that shelf, and moves its MEMORY.md line along.
func (d *Dashboard) move(w http.ResponseWriter, r *http.Request, machine string) {
	target, op, err := d.store.MoveNote(dashboardPrefix+machine, r.PathValue("path"), r.FormValue("to"))
	if err != nil {
		d.refused(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": target, "url": notePath(target), "op": op})
}

// deleteNote removes a note and its MEMORY.md line; both stay in the history.
func (d *Dashboard) deleteNote(w http.ResponseWriter, r *http.Request, machine string) {
	op, err := d.store.DeleteNote(dashboardPrefix+machine, r.PathValue("path"))
	if err != nil {
		d.refused(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"op": op, "url": "/"})
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
