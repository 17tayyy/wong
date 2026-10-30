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
	created_at TEXT NOT NULL,
	op         TEXT NOT NULL DEFAULT ''
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
	created_at TEXT NOT NULL,
	last_used  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS tasks (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	note       TEXT NOT NULL,
	project    TEXT NOT NULL,
	severity   TEXT NOT NULL,
	ref        TEXT NOT NULL,
	message    TEXT NOT NULL,
	source     TEXT NOT NULL,
	event      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	closed_at  TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS tasks_open ON tasks(note, ref) WHERE closed_at IS NULL;
CREATE INDEX IF NOT EXISTS tasks_project ON tasks(project) WHERE closed_at IS NULL;
CREATE TABLE IF NOT EXISTS findings (
	project     TEXT NOT NULL,
	machine     TEXT NOT NULL,
	note        TEXT NOT NULL,
	severity    TEXT NOT NULL,
	ref         TEXT NOT NULL,
	message     TEXT NOT NULL,
	reported_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS findings_project ON findings(project, machine);
CREATE INDEX IF NOT EXISTS findings_note ON findings(note);
CREATE TABLE IF NOT EXISTS sessions (
	hash       TEXT PRIMARY KEY,
	via        TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS hooks (
	name       TEXT PRIMARY KEY,
	secret     TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS keyring (
	device     TEXT PRIMARY KEY,
	public     BLOB NOT NULL,
	wrapped    BLOB,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	source     TEXT NOT NULL,
	remote     TEXT NOT NULL DEFAULT '',
	event      TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS recovery (
	id      INTEGER PRIMARY KEY CHECK (id = 1),
	public  BLOB NOT NULL,
	wrapped BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS cloud_ais (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	wrapped    BLOB NOT NULL,
	device     TEXT NOT NULL,
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

// ErrBadPush marks a push the server refuses as a whole: a path outside the brain, content that does not match its sha,
// or a file in the clear sent to a sealed brain.
var ErrBadPush = errors.New("bad push")

type Store struct {
	db     *sql.DB
	id     string
	limits Limits
	cloud  *CloudAIs
}

func Open(file string) (*Store, error) {
	db, err := sql.Open(sqlDriver, dataSource(file)+"_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=secure_delete(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &Store{db: db}, nil
}

// migrate creates the schema and adds the columns that databases made by older versions lack.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	for _, c := range []struct{ table, column, ddl string }{
		{"tokens", "last_used", `ALTER TABLE tokens ADD COLUMN last_used TEXT NOT NULL DEFAULT ''`},
		{"history", "op", `ALTER TABLE history ADD COLUMN op TEXT NOT NULL DEFAULT ''`},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(c.ddl); err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS history_op ON history(op) WHERE op != ''`)
	return err
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
	return s.apply(machine, "", pushes)
}

// ApplyOp stores changes to several files as one operation, undone as a whole: all of them, or none when any conflicts.
func (s *Store) ApplyOp(machine, op string, pushes []Upload) (conflicts []Change, err error) {
	_, conflicts, err = s.apply(machine, op, pushes)
	return conflicts, err
}

// NewOp names a new operation of a kind, such as rename or delete.
func NewOp(kind string) string {
	raw := make([]byte, 6)
	rand.Read(raw)
	return kind + ":" + hex.EncodeToString(raw)
}

func (s *Store) apply(machine, op string, pushes []Upload) (accepted []string, conflicts []Change, err error) {
	if err := s.room(pushes); err != nil {
		return nil, nil, err
	}
	sealed, err := s.Sealed()
	if err != nil {
		return nil, nil, err
	}
	if !sealed && s.limits.Sealed && len(pushes) > 0 {
		return nil, nil, ErrMustSeal
	}
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
		if sealed && !sealedID(p.Path) {
			return nil, nil, fmt.Errorf("%w: %s is not encrypted, and this brain is", ErrBadPush, p.Path)
		}
		if !sealed && p.Sha != "" && p.Sha != Sha(p.Content) {
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
		res, err := tx.Exec(`INSERT INTO history (path, sha, content, deleted, modified, machine, created_at, op) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Path, p.Sha, p.Content, p.Sha == "", p.Modified, machine, now, op)
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
		if !sealed {
			if err := recheck(tx, p.Path, p.Content, p.Modified, p.Sha == "", now); err != nil {
				return nil, nil, err
			}
		}
		if _, err := tx.Exec(`DELETE FROM findings WHERE note = ?`, p.Path); err != nil {
			return nil, nil, err
		}
		accepted = append(accepted, p.Path)
	}
	if op != "" && len(conflicts) > 0 {
		return nil, conflicts, nil
	}
	return accepted, conflicts, tx.Commit()
}

// NewToken creates a token for a machine or person and returns it; only its hash is stored.
// In a Folder, tokens name their account, so the server knows which brain they open.
func (s *Store) NewToken(name string) (string, error) {
	token, err := s.newSecret()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO tokens (name, hash, created_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET hash = excluded.hash, created_at = excluded.created_at, last_used = ''`,
		name, Sha([]byte(token)), time.Now().UTC().Format(time.RFC3339))
	return token, err
}

// newSecret makes a token or session for this brain: wong_<secret>, or wong_<account>_<secret> in a Folder.
func (s *Store) newSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	if s.id != "" {
		return "wong_" + s.id + "_" + hex.EncodeToString(raw), nil
	}
	return "wong_" + hex.EncodeToString(raw), nil
}

// Machine returns the name of the token's owner, or "" when the token is unknown, and notes when it was used.
func (s *Store) Machine(token string) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM tokens WHERE hash = ?`, Sha([]byte(token))).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
	_, err = s.db.Exec(`UPDATE tokens SET last_used = ? WHERE name = ? AND last_used <> ?`, now, name, now)
	return name, err
}

// TokenInfo is who holds a token and when it was created and last used; the token itself is never stored.
type TokenInfo struct {
	Name     string
	Created  string
	LastUsed string
}

func (s *Store) Tokens() ([]TokenInfo, error) {
	rows, err := s.db.Query(`SELECT name, created_at, last_used FROM tokens ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []TokenInfo
	for rows.Next() {
		var t TokenInfo
		if err := rows.Scan(&t.Name, &t.Created, &t.LastUsed); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeToken deletes a token, and reports whether there was one with that name.
func (s *Store) RevokeToken(name string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	if err := s.ForgetKey(name); err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
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
