package remote

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/17tayyy/wong/internal/check"
)

// ErrNotLatest marks an undo of a change that a later change to the same file already replaced.
var ErrNotLatest = errors.New("a later change to this file exists")

// Version is one stored version of a file of the brain: who wrote it and when.
type Version struct {
	Seq      int64
	Path     string
	Sha      string
	Deleted  bool
	Machine  string
	Created  time.Time
	Modified string
	Op       string
}

const versionCols = `seq, path, sha, deleted, machine, created_at, modified, op`

func scanVersion(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	var created string
	if err := row.Scan(&v.Seq, &v.Path, &v.Sha, &v.Deleted, &v.Machine, &created, &v.Modified, &v.Op); err != nil {
		return v, err
	}
	v.Created, _ = time.Parse(time.RFC3339, created)
	return v, nil
}

func (s *Store) versions(query string, args ...any) ([]Version, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Files returns the current files of the brain with their content, deleted ones left out, sorted by path.
func (s *Store) Files() ([]Change, error) {
	rows, err := s.db.Query(`SELECT path, sha, content, modified, seq FROM files WHERE deleted = 0 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []Change
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Path, &c.Sha, &c.Content, &c.Modified, &c.Seq); err != nil {
			return nil, err
		}
		files = append(files, c)
	}
	return files, rows.Err()
}

// History returns every version of a file, newest first.
func (s *Store) History(path string) ([]Version, error) {
	return s.versions(`SELECT `+versionCols+` FROM history WHERE path = ? ORDER BY seq DESC`, path)
}

// Log returns up to limit changes across the brain, newest first, older than before when before is not 0.
func (s *Store) Log(limit int, before int64) ([]Version, error) {
	if before <= 0 {
		before = 1 << 62
	}
	return s.versions(`SELECT `+versionCols+` FROM history WHERE seq < ? ORDER BY seq DESC LIMIT ?`, before, limit)
}

// Version returns one stored version and its content.
func (s *Store) Version(seq int64) (Version, []byte, error) {
	v, err := scanVersion(s.db.QueryRow(`SELECT `+versionCols+` FROM history WHERE seq = ?`, seq))
	if err != nil {
		return v, nil, err
	}
	var content []byte
	err = s.db.QueryRow(`SELECT content FROM history WHERE seq = ?`, seq).Scan(&content)
	return v, content, err
}

// Previous returns the version a change replaced, or ok false when the change created the file.
func (s *Store) Previous(v Version) (Version, []byte, bool, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT seq FROM history WHERE path = ? AND seq < ? ORDER BY seq DESC LIMIT 1`, v.Path, v.Seq).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, nil, false, nil
	}
	if err != nil {
		return Version{}, nil, false, err
	}
	prev, content, err := s.Version(seq)
	return prev, content, true, err
}

// Undo reverts one change, as a new change by machine: the file goes back to the version before it, or away when the change created it.
func (s *Store) Undo(seq int64, machine string) error {
	v, _, err := s.Version(seq)
	if err != nil {
		return err
	}
	var current int64
	if err := s.db.QueryRow(`SELECT seq FROM files WHERE path = ?`, v.Path).Scan(&current); err != nil {
		return err
	}
	if current != seq {
		return fmt.Errorf("%s: %w", v.Path, ErrNotLatest)
	}
	prev, content, ok, err := s.Previous(v)
	if err != nil {
		return err
	}
	up := Upload{Path: v.Path, Base: v.Sha, Modified: time.Now().UTC().Format(time.RFC3339Nano)}
	if v.Deleted {
		up.Base = ""
	}
	if ok && !prev.Deleted {
		up.Sha, up.Content = prev.Sha, content
	}
	_, conflicts, err := s.Apply(machine, []Upload{up})
	if err == nil && len(conflicts) > 0 {
		err = fmt.Errorf("%s: %w", v.Path, ErrNotLatest)
	}
	return err
}

// Restore makes a past version the current one, as a new change by machine, unless the file changed since base.
func (s *Store) Restore(seq int64, base, machine string) error {
	v, content, err := s.Version(seq)
	if err != nil {
		return err
	}
	up := Upload{Path: v.Path, Base: base, Modified: time.Now().UTC().Format(time.RFC3339Nano)}
	if !v.Deleted {
		up.Sha, up.Content = v.Sha, content
	}
	_, conflicts, err := s.Apply(machine, []Upload{up})
	if err == nil && len(conflicts) > 0 {
		err = fmt.Errorf("%s: %w", v.Path, ErrNotLatest)
	}
	return err
}

// Op returns the versions an operation wrote, in order.
func (s *Store) Op(op string) ([]Version, error) {
	return s.versions(`SELECT `+versionCols+` FROM history WHERE op = ? ORDER BY seq`, op)
}

// UndoOp reverts every change of an operation, as a new operation by machine, unless a later change replaced any of them.
func (s *Store) UndoOp(op, machine string) (string, error) {
	vs, err := s.Op(op)
	if err != nil {
		return "", err
	}
	if len(vs) == 0 {
		return "", sql.ErrNoRows
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var ups []Upload
	for _, v := range vs {
		prev, content, ok, err := s.Previous(v)
		if err != nil {
			return "", err
		}
		up := Upload{Path: v.Path, Base: v.Sha, Modified: now}
		if ok && !prev.Deleted {
			up.Sha, up.Content = prev.Sha, content
		}
		ups = append(ups, up)
	}
	undo := NewOp("undo")
	conflicts, err := s.ApplyOp(machine, undo, ups)
	if err == nil && len(conflicts) > 0 {
		err = fmt.Errorf("%s: %w", conflicts[0].Path, ErrNotLatest)
	}
	return undo, err
}

// ReportFindings replaces what a machine's check last reported for a project.
func (s *Store) ReportFindings(machine, project string, findings []check.Finding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM findings WHERE project = ? AND machine = ?`, project, machine); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, f := range findings {
		if p, ok := NoteProject(f.Note); !ok || p != project {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO findings (project, machine, note, severity, ref, message, reported_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			project, machine, f.Note, string(f.Severity), f.Ref, f.Message, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Findings returns what machines' checks reported for a project, or for every project when project is "", newest report first for each note and ref.
func (s *Store) Findings(project string) ([]check.Finding, error) {
	rows, err := s.db.Query(`SELECT note, severity, ref, message FROM findings
		WHERE ? = '' OR project = ? ORDER BY note, ref, reported_at DESC`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []check.Finding
	for rows.Next() {
		var f check.Finding
		var sev string
		if err := rows.Scan(&f.Note, &sev, &f.Ref, &f.Message); err != nil {
			return nil, err
		}
		f.Severity = check.Severity(sev)
		if n := len(out); n > 0 && out[n-1].Note == f.Note && out[n-1].Ref == f.Ref {
			continue
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
