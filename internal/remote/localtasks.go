package remote

import (
	"context"
	"encoding/json"
	"errors"
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

// Events returns the webhook events a sealed brain kept after since, and the cursor to ask from next time.
func (c *Client) Events(ctx context.Context, since int64) ([]Event, int64, error) {
	var out eventsResponse
	err := c.do(ctx, http.MethodGet, "/api/events?since="+url.QueryEscape(strconv.FormatInt(since, 10)), nil, &out)
	return out.Events, out.Cursor, err
}

// SyncTasks applies the webhook events a sealed brain kept since the last time to this machine's copy of its notes,
// as the server does for a brain in the clear: a task opens on each note an event leaves something to fix in, and
// closes once the note no longer does. GitHub events reach the project the brain's remotes.yaml maps their repo to.
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
	for _, e := range events {
		var ev event
		if err := json.Unmarshal(e.Event, &ev); err != nil {
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
	return st.save(brain)
}

// OpenTasks returns the open tasks of a project, "me" for the global notes: the server's, or for a sealed brain the
// ones this machine keeps, after applying the events it had not fetched yet.
func OpenTasks(ctx context.Context, c *Client, brain, project string) ([]Task, error) {
	if err := c.Unlock(ctx); err != nil {
		return nil, err
	}
	if c.Key == nil {
		return c.Tasks(ctx, project)
	}
	if err := SyncTasks(ctx, c, brain); err != nil {
		return nil, err
	}
	return LocalTasks(brain, project)
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
