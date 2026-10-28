package remote

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/repos"
)

// Task is a finding a webhook left on a note, for the agent working on its project to fix.
type Task struct {
	ID       int64  `json:"id"`
	Note     string `json:"note"`
	Project  string `json:"project"`
	Severity string `json:"severity"`
	Ref      string `json:"ref"`
	Message  string `json:"message"`
	Source   string `json:"source"`
	Created  string `json:"created"`
}

// event is what a webhook reported, kept with its tasks to apply the rule again when their note changes.
type event struct {
	Issue  *linear.Issue `json:"issue,omitempty"`
	Repo   string        `json:"repo,omitempty"`
	Path   string        `json:"path,omitempty"`
	Commit string        `json:"commit,omitempty"`
	Tag    string        `json:"tag,omitempty"`
	Branch string        `json:"branch,omitempty"`
	By     string        `json:"by,omitempty"`
	When   time.Time     `json:"when,omitzero"`
}

func (e event) apply(n check.Note) (check.Finding, bool, error) {
	switch {
	case e.Issue != nil:
		return check.IssueClosed(n, *e.Issue)
	case e.Tag != "":
		return check.Released(n, e.Repo, e.Tag, e.By, e.When)
	case e.Branch != "":
		return check.Pushed(n, e.Repo, e.Branch, e.Commit, e.By, e.When)
	}
	return check.FileRemoved(n, e.Repo, e.Path, e.Commit, e.When)
}

// NoteProject returns the folder of a note of the brain: "me" or a project id.
func NoteProject(p string) (string, bool) {
	dir, file := path.Split(p)
	if path.Ext(file) != ".md" || file == "MEMORY.md" {
		return "", false
	}
	if dir == "me/" {
		return "me", true
	}
	id, ok := strings.CutPrefix(dir, "projects/")
	if !ok || strings.Count(id, "/") != 1 {
		return "", false
	}
	return strings.TrimSuffix(id, "/"), true
}

// notes returns the notes of a project, or of the whole brain when project is "".
func (s *Store) notes(project string) ([]check.Note, error) {
	rows, err := s.db.Query(`SELECT path, content, modified FROM files WHERE deleted = 0 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []check.Note
	for rows.Next() {
		var n check.Note
		var modified string
		if err := rows.Scan(&n.Path, &n.Data, &modified); err != nil {
			return nil, err
		}
		if id, ok := NoteProject(n.Path); ok && (project == "" || id == project) {
			n.Written, _ = time.Parse(time.RFC3339Nano, modified)
			notes = append(notes, n)
		}
	}
	return notes, rows.Err()
}

// remotes reads the brain's remotes.yaml.
func (s *Store) remotes() (repos.Remotes, error) {
	var data []byte
	err := s.db.QueryRow(`SELECT content FROM files WHERE path = ? AND deleted = 0`, repos.RemotesFile).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return repos.Remotes{}, nil
	}
	if err != nil {
		return nil, err
	}
	return repos.ParseRemotes(data)
}

// addTasks applies an event's rule to notes and opens a task for each finding, or refreshes the open one.
func (s *Store) addTasks(ev event, source string, notes []check.Note) (int, error) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	added := 0
	for _, n := range notes {
		f, found, err := ev.apply(n)
		if err != nil || !found {
			continue
		}
		project, _ := NoteProject(n.Path)
		if _, err := tx.Exec(`INSERT INTO tasks (note, project, severity, ref, message, source, event, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(note, ref) WHERE closed_at IS NULL DO UPDATE SET severity = excluded.severity, message = excluded.message,
				source = excluded.source, event = excluded.event`,
			n.Path, project, string(f.Severity), f.Ref, f.Message, source, string(raw), now); err != nil {
			return 0, err
		}
		added++
	}
	return added, tx.Commit()
}

// recheck applies again the rule of each open task of a note that has just changed, and closes the ones it no longer breaks.
func recheck(tx *sql.Tx, p string, content []byte, modified string, deleted bool, now string) error {
	rows, err := tx.Query(`SELECT id, event FROM tasks WHERE note = ? AND closed_at IS NULL`, p)
	if err != nil {
		return err
	}
	type open struct {
		id  int64
		raw string
	}
	var tasks []open
	for rows.Next() {
		var t open
		if err := rows.Scan(&t.id, &t.raw); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	written, _ := time.Parse(time.RFC3339Nano, modified)
	for _, t := range tasks {
		var (
			ev    event
			f     check.Finding
			found bool
		)
		if !deleted && json.Unmarshal([]byte(t.raw), &ev) == nil {
			f, found, _ = ev.apply(check.Note{Path: p, Data: content, Written: written})
		}
		if found {
			_, err = tx.Exec(`UPDATE tasks SET severity = ?, message = ? WHERE id = ?`, string(f.Severity), f.Message, t.id)
		} else {
			_, err = tx.Exec(`UPDATE tasks SET closed_at = ? WHERE id = ?`, now, t.id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Tasks returns the open tasks of a project ("me" for the global notes), or of every project when project is "".
func (s *Store) Tasks(project string) ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, note, project, severity, ref, message, source, created_at FROM tasks
		WHERE closed_at IS NULL AND (? = '' OR project = ?) ORDER BY note, id`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Note, &t.Project, &t.Severity, &t.Ref, &t.Message, &t.Source, &t.Created); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
