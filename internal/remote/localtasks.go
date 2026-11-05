package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/repos"
)

const tasksPath = ".wong/tasks.json"

// localTasks are the tasks a machine keeps for a sealed brain, whose server holds only the webhook events.
type localTasks struct {
	Cursor int64       `json:"cursor"`
	Tasks  []localTask `json:"tasks,omitempty"`
}

type localTask struct {
	Task
	Event json.RawMessage `json:"event"`
}

// AnswerCheck tells the server what comparing a pull request with this machine's copy of the notes found.
func (c *Client) AnswerCheck(ctx context.Context, a CheckAnswer) error {
	return c.do(ctx, http.MethodPost, "/api/checks", a, &struct{}{})
}

// Events returns the webhook events a sealed brain kept after since, and the cursor to ask from next time.
func (c *Client) Events(ctx context.Context, since int64) ([]Event, int64, error) {
	var out eventsResponse
	err := c.do(ctx, http.MethodGet, "/api/events?since="+url.QueryEscape(strconv.FormatInt(since, 10)), nil, &out)
	return out.Events, out.Cursor, err
}

// SyncTasks applies the webhook events a sealed brain kept since the last time to this machine's copy of its notes,
// as the server does for a brain in the clear: a task opens on each note an event leaves something to fix in, and
// closes once the note no longer does. GitHub events reach the project the brain's remotes.yaml maps their repo to.
// A pull request waiting for wong's check is compared with the notes of that project, and the counts sent back.
func SyncTasks(ctx context.Context, c *Client, brain string) error {
	st, err := loadTasks(brain)
	if err != nil {
		return err
	}
	events, cursor, err := c.Events(ctx, st.Cursor)
	if err != nil {
		return err
	}
	notes, err := localNotes(brain)
	if err != nil {
		return err
	}
	remotes := repos.Remotes{}
	if data, err := os.ReadFile(filepath.Join(brain, repos.RemotesFile)); err == nil {
		if remotes, err = repos.ParseRemotes(data); err != nil {
			return err
		}
	}
	var answerErr error
	for _, e := range events {
		var ev event
		if err := json.Unmarshal(e.Event, &ev); err != nil {
			continue
		}
		if ev.PR != nil {
			answer := CheckAnswer{}
			if r, ok := remotes.Lookup(e.Remote); ok {
				answer = prReport(projectNotes(notes, r.Project), r.Dir, ev.PR.Files)
			}
			answer.Check = ev.PR.Check
			answerErr = errors.Join(answerErr, c.AnswerCheck(ctx, answer))
			continue
		}
		project := ""
		if e.Remote != "" {
			r, ok := remotes.Lookup(e.Remote)
			if !ok {
				continue
			}
			ev.Repo, project = r.Dir, r.Project
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		for _, n := range notes {
			id, _ := NoteProject(n.Path)
			if project != "" && id != project {
				continue
			}
			if f, found, err := ev.apply(n); err == nil && found {
				st.open(Task{Note: n.Path, Project: id, Severity: string(f.Severity), Ref: f.Ref, Message: f.Message, Source: e.Source, Created: e.Created}, raw)
			}
		}
	}
	byPath := map[string]check.Note{}
	for _, n := range notes {
		byPath[n.Path] = n
	}
	kept := st.Tasks[:0]
	for _, t := range st.Tasks {
		var ev event
		n, ok := byPath[t.Note]
		if !ok || json.Unmarshal(t.Event, &ev) != nil {
			continue
		}
		if f, found, err := ev.apply(n); err == nil && found {
			t.Severity, t.Message = string(f.Severity), f.Message
			kept = append(kept, t)
		}
	}
	st.Tasks, st.Cursor = kept, cursor
	return errors.Join(st.save(brain), answerErr)
}

func projectNotes(notes []check.Note, project string) []check.Note {
	var out []check.Note
	for _, n := range notes {
		if id, _ := NoteProject(n.Path); id == project {
			out = append(out, n)
		}
	}
	return out
}

// OpenTasks returns the open tasks of a project, "me" for the global notes: the server's, or for a sealed brain the
// ones this machine keeps, after applying the events it had not fetched yet. On the way it takes into the brain what
// the server holds for this machine, such as Linear's token.
func OpenTasks(ctx context.Context, c *Client, brain, project string) ([]Task, error) {
	if err := c.Unlock(ctx); err != nil {
		return nil, err
	}
	var handoffErr error
	if err := TakeHandoff(ctx, c, brain); err != nil {
		handoffErr = fmt.Errorf("Linear's token: %w", err)
	}
	if c.Key == nil {
		tasks, err := c.Tasks(ctx, project)
		return tasks, errors.Join(err, handoffErr)
	}
	if err := SyncTasks(ctx, c, brain); err != nil {
		return nil, errors.Join(err, handoffErr)
	}
	tasks, err := LocalTasks(brain, project)
	return tasks, errors.Join(err, handoffErr)
}

// MirrorTasks copies the open tasks this machine keeps for a sealed brain into store, the server of its decrypted copy
// on this machine, and closes there the ones it no longer keeps, so that the copy's dashboard shows what is open and
// what was fixed.
func MirrorTasks(store *Store, brain string) error {
	st, err := loadTasks(brain)
	if err != nil {
		return err
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[[2]string]bool{}
	for _, t := range st.Tasks {
		keep[[2]string{t.Note, t.Ref}] = true
		if _, err := tx.Exec(`INSERT INTO tasks (note, project, severity, ref, message, source, event, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(note, ref) WHERE closed_at IS NULL DO UPDATE SET severity = excluded.severity, message = excluded.message,
				source = excluded.source, event = excluded.event`,
			t.Note, t.Project, t.Severity, t.Ref, t.Message, t.Source, string(t.Event), t.Created); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT id, note, ref FROM tasks WHERE closed_at IS NULL`)
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		var n, ref string
		if err := rows.Scan(&id, &n, &ref); err != nil {
			rows.Close()
			return err
		}
		if !keep[[2]string{n, ref}] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range gone {
		if _, err := tx.Exec(`UPDATE tasks SET closed_at = ? WHERE id = ?`, utcNow(), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LocalTasks returns the open tasks this machine keeps for a sealed brain, of a project ("me" for the global notes),
// or of every project when project is "".
func LocalTasks(brain, project string) ([]Task, error) {
	st, err := loadTasks(brain)
	if err != nil {
		return nil, err
	}
	tasks := []Task{}
	for i, t := range st.Tasks {
		if project == "" || t.Project == project {
			t.ID = int64(i + 1)
			tasks = append(tasks, t.Task)
		}
	}
	return tasks, nil
}

func (st *localTasks) open(t Task, raw json.RawMessage) {
	i := slices.IndexFunc(st.Tasks, func(o localTask) bool { return o.Note == t.Note && o.Ref == t.Ref })
	if i < 0 {
		st.Tasks = append(st.Tasks, localTask{t, raw})
		return
	}
	t.Created = st.Tasks[i].Created
	st.Tasks[i] = localTask{t, raw}
}

func loadTasks(brain string) (localTasks, error) {
	var st localTasks
	data, err := os.ReadFile(filepath.Join(brain, tasksPath))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

func (st localTasks) save(brain string) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(brain, tasksPath), append(data, '\n'), "")
}

// localNotes reads the notes of the brain's shelves on this machine, with the time each was last written.
func localNotes(brain string) ([]check.Note, error) {
	var notes []check.Note
	err := filepath.WalkDir(brain, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel(brain, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(rel, ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := NoteProject(rel); !ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		notes = append(notes, check.Note{Path: rel, Data: data, Written: info.ModTime().UTC()})
		return nil
	})
	return notes, err
}
