// Package remote keeps the brain on a wong server: the server's store and API, and the client that syncs with it.
package remote

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS history (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	path       TEXT NOT NULL,
	sha        TEXT NOT NULL,
	content    BLOB,
	deleted    INTEGER NOT NULL DEFAULT 0,
	modified   TEXT NOT NULL DEFAULT '',
	machine    TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS history_path ON history(path, seq);
CREATE TABLE IF NOT EXISTS files (
	path    TEXT PRIMARY KEY,
	sha     TEXT NOT NULL,
	content  BLOB,
	deleted  INTEGER NOT NULL DEFAULT 0,
	modified TEXT NOT NULL DEFAULT '',
	seq      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS files_seq ON files(seq);
CREATE TABLE IF NOT EXISTS tokens (
	name       TEXT PRIMARY KEY,
	hash       TEXT NOT NULL,
	created_at TEXT NOT NULL
);`

// Change is the current state of one file of the brain on the server.
type Change struct {
	Path     string `json:"path"`
	Sha      string `json:"sha,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	Content  []byte `json:"content,omitempty"`
	Modified string `json:"modified,omitempty"`
	Seq      int64  `json:"seq"`
}

// Upload is a change a client sends, based on the version it last synced (Base, empty for a new file).
type Upload struct {
	Path     string `json:"path"`
	Base     string `json:"base,omitempty"`
	Sha      string `json:"sha,omitempty"`
	Content  []byte `json:"content,omitempty"`
	Modified string `json:"modified,omitempty"`
}

// ErrBadPush marks a push the server refuses as a whole: a path outside the brain or content that does not match its sha.
var ErrBadPush = errors.New("bad push")

type Store struct {
	db *sql.DB
}

func Open(file string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Changes returns every file changed after since, and the cursor to ask from next time.
func (s *Store) Changes(since int64) ([]Change, int64, error) {
	rows, err := s.db.Query(`SELECT path, sha, content, deleted, modified, seq FROM files WHERE seq > ? ORDER BY seq`, since)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var changes []Change
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Path, &c.Sha, &c.Content, &c.Deleted, &c.Modified, &c.Seq); err != nil {
			return nil, 0, err
		}
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var cursor sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM history`).Scan(&cursor); err != nil {
		return nil, 0, err
	}
	return changes, max(cursor.Int64, since), nil
}

// Apply stores the changes whose base is the server's current version and returns the others as conflicts.
func (s *Store) Apply(machine string, pushes []Upload) (accepted []string, conflicts []Change, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, p := range pushes {
		if err := ValidPath(p.Path); err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrBadPush, err)
		}
		if p.Sha != "" && p.Sha != Sha(p.Content) {
			return nil, nil, fmt.Errorf("%w: %s: content does not match its sha", ErrBadPush, p.Path)
		}
		var current Change
		err := tx.QueryRow(`SELECT path, sha, content, deleted, modified, seq FROM files WHERE path = ?`, p.Path).
			Scan(&current.Path, &current.Sha, &current.Content, &current.Deleted, &current.Modified, &current.Seq)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			current = Change{Path: p.Path, Deleted: true}
		case err != nil:
			return nil, nil, err
		}
		if current.Deleted {
			current.Sha, current.Content = "", nil
		}
		if p.Base != current.Sha {
			conflicts = append(conflicts, current)
			continue
		}
		if p.Sha == current.Sha {
			accepted = append(accepted, p.Path)
			continue
		}
		res, err := tx.Exec(`INSERT INTO history (path, sha, content, deleted, modified, machine, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.Path, p.Sha, p.Content, p.Sha == "", p.Modified, machine, now)
		if err != nil {
			return nil, nil, err
		}
		seq, err := res.LastInsertId()
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(`INSERT INTO files (path, sha, content, deleted, modified, seq) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET sha = excluded.sha, content = excluded.content, deleted = excluded.deleted,
				modified = excluded.modified, seq = excluded.seq`,
			p.Path, p.Sha, p.Content, p.Sha == "", p.Modified, seq); err != nil {
			return nil, nil, err
		}
		accepted = append(accepted, p.Path)
	}
	return accepted, conflicts, tx.Commit()
}

// NewToken creates a token for a machine or person and returns it; only its hash is stored.
func (s *Store) NewToken(name string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := "wong_" + hex.EncodeToString(raw)
	_, err := s.db.Exec(`INSERT INTO tokens (name, hash, created_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET hash = excluded.hash, created_at = excluded.created_at`,
		name, Sha([]byte(token)), time.Now().UTC().Format(time.RFC3339))
	return token, err
}

// Machine returns the name of the token's owner, or "" when the token is unknown.
func (s *Store) Machine(token string) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM tokens WHERE hash = ?`, Sha([]byte(token))).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func Sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ValidPath accepts a clean path inside the brain, outside its local .git and .wong folders.
func ValidPath(p string) error {
	clean := path.Clean(p)
	if p == "" || clean != p || path.IsAbs(p) || strings.HasPrefix(p, "../") || p == ".." ||
		strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, ".wong/") || strings.Contains(p, "\\") {
		return fmt.Errorf("invalid path %q", p)
	}
	return nil
}
