package remote

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/seal"
)

var (
	// ErrNotMoving refuses history for a brain that already holds some of its own, or whose move is over.
	ErrNotMoving = errors.New("a brain moves only into an empty one, and only until its move is done")
)

// Entry is one version of a file in a brain's history, as one server hands it to another when the brain moves.
type Entry struct {
	Seq      int64  `json:"seq"`
	Path     string `json:"path"`
	Sha      string `json:"sha,omitempty"`
	Content  []byte `json:"content,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	Modified string `json:"modified,omitempty"`
	Machine  string `json:"machine"`
	Created  string `json:"created"`
	Op       string `json:"op,omitempty"`
}

// ExportHistory returns up to limit versions of the brain's history after the one numbered after, oldest first.
func (s *Store) ExportHistory(after int64, limit int) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT seq, path, sha, content, deleted, modified, machine, created_at, op FROM history WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Seq, &e.Path, &e.Sha, &e.Content, &e.Deleted, &e.Modified, &e.Machine, &e.Created, &e.Op); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ImportHistory replays, oldest first, the history of a brain moving here from another server, keeping who made each
// version and when. Only a brain with no history takes the first entries, and it takes more until done; a sealed
// brain takes only sealed entries, and a server that keeps brains sealed takes none in the clear.
func (s *Store) ImportHistory(entries []Entry, done bool) error {
	if err := s.writable(); err != nil {
		return err
	}
	ups := make([]Upload, len(entries))
	for i, e := range entries {
		ups[i] = Upload{Path: e.Path, Content: e.Content}
	}
	if err := s.room(ups); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var moving sql.NullBool
	if err := tx.QueryRow(`SELECT done FROM moving`).Scan(&moving); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	switch {
	case moving.Valid && moving.Bool:
		return ErrNotMoving
	case !moving.Valid:
		var held int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&held); err != nil {
			return err
		}
		if held > 0 {
			return ErrNotMoving
		}
		if _, err := tx.Exec(`INSERT INTO moving (id, done, started_at) VALUES (1, 0, ?)`, utcNow()); err != nil {
			return err
		}
	}
	var sealed int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM recovery`).Scan(&sealed); err != nil {
		return err
	}
	if sealed == 0 && s.limits.Sealed && len(entries) > 0 {
		return ErrMustSeal
	}
	for _, e := range entries {
		switch {
		case sealed > 0 && !sealedID(e.Path):
			return refuse(ErrBadPush, "%s is a file in the clear, and this brain is end-to-end encrypted.", e.Path)
		case sealed == 0:
			if err := ValidPath(e.Path); err != nil {
				return refuse(ErrBadPush, "%v", err)
			}
		}
		if _, err := time.Parse(time.RFC3339, e.Created); err != nil {
			return refuse(ErrBadPush, "%s has no valid date.", e.Path)
		}
		res, err := tx.Exec(`INSERT INTO history (path, sha, content, deleted, modified, machine, created_at, op) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			e.Path, e.Sha, nullIfEmpty(e.Content, e.Deleted), e.Deleted, e.Modified, strings.TrimSpace(e.Machine), e.Created, e.Op)
		if err != nil {
			return err
		}
		seq, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO files (path, sha, content, deleted, modified, seq) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET sha = excluded.sha, content = excluded.content, deleted = excluded.deleted,
				modified = excluded.modified, seq = excluded.seq`,
			e.Path, e.Sha, nullIfEmpty(e.Content, e.Deleted), e.Deleted, e.Modified, seq); err != nil {
			return err
		}
	}
	if done {
		if _, err := tx.Exec(`UPDATE moving SET done = 1`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullIfEmpty(content []byte, deleted bool) any {
	if deleted {
		return nil
	}
	return content
}

// SetMovedTo records that the brain moved to another server, for the machines still syncing here to hear of it.
func (s *Store) SetMovedTo(url string) error {
	if err := CheckURL(url); err != nil {
		return fmt.Errorf("%w: %v", ErrBadPush, err)
	}
	_, err := s.db.Exec(`INSERT INTO moved (id, url, moved_at) VALUES (1, ?, ?) ON CONFLICT(id) DO UPDATE SET url = excluded.url, moved_at = excluded.moved_at`, url, utcNow())
	return err
}

// MovedTo returns where the brain moved, or "" while it lives here.
func (s *Store) MovedTo() (string, error) {
	var url string
	err := s.db.QueryRow(`SELECT url FROM moved`).Scan(&url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return url, err
}

// CopyHistory hands the whole history of a brain from one server to another, oldest first, sealing each version on the
// way when seals is set, and ends the move; progress hears how many versions moved so far. It returns that count.
func CopyHistory(ctx context.Context, from, to *Client, seals *seal.Key, progress func(int)) (int, error) {
	moved := 0
	var after int64
	for {
		entries, err := from.ExportHistory(ctx, after)
		if err != nil {
			return moved, err
		}
		if len(entries) == 0 {
			break
		}
		after = entries[len(entries)-1].Seq
		if seals != nil {
			for i, e := range entries {
				path := e.Path
				e.Path, e.Sha = seals.ID(path), ""
				if !e.Deleted {
					e.Sha = seals.Sum(e.Content)
					if _, e.Content, err = seals.Seal(path, e.Content); err != nil {
						return moved, err
					}
				}
				entries[i] = e
			}
		}
		if err := to.ImportHistory(ctx, entries, false); err != nil {
			return moved, err
		}
		moved += len(entries)
		if progress != nil {
			progress(moved)
		}
	}
	return moved, to.ImportHistory(ctx, nil, true)
}
