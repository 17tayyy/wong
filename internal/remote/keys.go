package remote

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// SealHeader is what a client that encrypts sends with every call; a sealed brain answers no other.
const SealHeader = "Wong-Seal"

var (
	// ErrSealed refuses to seal a brain that already is.
	ErrSealed = errors.New("this brain is already end-to-end encrypted")
	// ErrBehind refuses to seal a brain whose files changed on the server after the machine sealing it pulled them.
	ErrBehind = errors.New("this brain changed on the server after this machine pulled it: pull and seal it again")
	// ErrNoKey refuses an approval from a device that does not hold the brain's key, or for one that did not ask.
	ErrNoKey = errors.New("no such key or request")
	// ErrOldClient refuses a client that does not encrypt, so it cannot write files in the clear into a sealed brain.
	ErrOldClient = errors.New("this brain is end-to-end encrypted: update wong to sync it")
	// ErrMustSeal refuses files for a brain that is not sealed yet, on a server that keeps every brain sealed.
	ErrMustSeal = errors.New("this server keeps brains end-to-end encrypted: seal this one first with wong keys seal")
)

// KeyRequest is a device that asked for the brain's key and waits for one that holds it to approve.
type KeyRequest struct {
	Device  string `json:"device"`
	Public  []byte `json:"public"`
	Created string `json:"created"`
}

// Keys is what a device learns about the keyring: whether the brain is sealed or must be, the key wrapped for it, the
// key wrapped for the recovery code, and the devices waiting for theirs.
type Keys struct {
	Sealed      bool         `json:"sealed"`
	Required    bool         `json:"required,omitempty"`
	Wrapped     []byte       `json:"wrapped,omitempty"`
	Recovery    []byte       `json:"recovery,omitempty"`
	Pending     []KeyRequest `json:"pending,omitempty"`
	CloudAIs    bool         `json:"cloud_ais,omitempty"`
	CloudPublic []byte       `json:"cloud_public,omitempty"`
}

// Sealed reports whether the brain is end-to-end encrypted, which it is from the moment its recovery key is kept.
func (s *Store) Sealed() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM recovery`).Scan(&n)
	return n > 0, err
}

// Keys returns the keyring as device sees it.
func (s *Store) Keys(device string) (Keys, error) {
	var k Keys
	err := s.db.QueryRow(`SELECT wrapped FROM recovery`).Scan(&k.Recovery)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return k, err
	}
	k.Sealed, k.Required = k.Recovery != nil, s.limits.Sealed
	if s.cloud != nil {
		k.CloudPublic = s.cloud.Public()
	}
	var cloud int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM cloud_ais`).Scan(&cloud); err != nil {
		return k, err
	}
	k.CloudAIs = cloud > 0
	rows, err := s.db.Query(`SELECT device, public, wrapped, created_at FROM keyring ORDER BY created_at, device`)
	if err != nil {
		return k, err
	}
	defer rows.Close()
	for rows.Next() {
		var r KeyRequest
		var wrapped []byte
		if err := rows.Scan(&r.Device, &r.Public, &wrapped, &r.Created); err != nil {
			return k, err
		}
		switch {
		case wrapped == nil:
			k.Pending = append(k.Pending, r)
		case r.Device == device:
			k.Wrapped = wrapped
		}
	}
	return k, rows.Err()
}

// StartSeal makes the brain end-to-end encrypted, with its key wrapped for the device that seals it and for the
// recovery code. A brain that holds files in the clear is sealed only by a machine that pulled them all, up to cursor,
// to push them again encrypted: their clear copies and history go, with the findings and tasks that name them, and the
// database is rebuilt so that no free page or log keeps them; the events behind the open tasks stay for the machines.
func (s *Store) StartSeal(device string, public, wrapped, recoveryPublic, recoveryWrapped []byte, cursor int64) error {
	if len(wrapped) == 0 || len(recoveryWrapped) == 0 {
		return ErrNoKey
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sealed int
	var latest sql.NullInt64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM recovery`).Scan(&sealed); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT MAX(seq) FROM history`).Scan(&latest); err != nil {
		return err
	}
	switch {
	case sealed > 0:
		return ErrSealed
	case latest.Int64 > cursor:
		return ErrBehind
	}
	if err := forgetClear(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO recovery (id, public, wrapped) VALUES (1, ?, ?)`, recoveryPublic, recoveryWrapped); err != nil {
		return err
	}
	if err := putKey(tx, device, public, wrapped); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if latest.Valid {
		s.db.Exec(`VACUUM`)
		s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return nil
}

// forgetClear drops what a brain in the clear keeps of its notes, keeping as events what its open tasks came from.
func forgetClear(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT DISTINCT source, event FROM tasks WHERE closed_at IS NULL`)
	if err != nil {
		return err
	}
	type kept struct{ source, event string }
	var events []kept
	for rows.Next() {
		var k kept
		if err := rows.Scan(&k.source, &k.event); err != nil {
			rows.Close()
			return err
		}
		events = append(events, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range events {
		remote := ""
		if repo, ok := strings.CutPrefix(k.source, "github "); ok {
			remote, _, _ = strings.Cut(repo, "@")
		}
		if _, err := tx.Exec(`INSERT INTO events (source, remote, event, created_at) VALUES (?, ?, ?, ?)`, k.source, remote, k.event, utcNow()); err != nil {
			return err
		}
	}
	for _, table := range []string{"files", "history", "findings", "tasks"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}
	return nil
}

// RequestKey records that device waits for the brain's key, wrapped for its public key.
func (s *Store) RequestKey(device string, public []byte) error {
	if len(public) == 0 {
		return ErrNoKey
	}
	_, err := s.db.Exec(`INSERT INTO keyring (device, public, wrapped, created_at, updated_at) VALUES (?, ?, NULL, ?, ?)
		ON CONFLICT(device) DO UPDATE SET public = excluded.public, wrapped = NULL, updated_at = excluded.updated_at`,
		device, public, utcNow(), utcNow())
	return err
}

// ApproveKey stores the key that approver, a device holding it, wrapped for a device that asked with public: a public
// key changed since it was shown to the person is not approved.
func (s *Store) ApproveKey(approver, device string, public, wrapped []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var holds int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM keyring WHERE device = ? AND wrapped IS NOT NULL`, approver).Scan(&holds); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE keyring SET wrapped = ?, updated_at = ? WHERE device = ? AND public = ? AND wrapped IS NULL`,
		wrapped, utcNow(), device, public)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 || holds == 0 || len(wrapped) == 0 {
		return errors.Join(err, ErrNoKey)
	}
	return tx.Commit()
}

// KeepOwnKey stores the key a device wrapped for itself after opening it with the recovery code.
func (s *Store) KeepOwnKey(device string, public, wrapped []byte) error {
	if len(wrapped) == 0 {
		return ErrNoKey
	}
	sealed, err := s.Sealed()
	if err != nil || !sealed {
		return errors.Join(err, ErrNoKey)
	}
	return putKey(s.db, device, public, wrapped)
}

// ForgetKey drops a device from the keyring, as when its token is revoked.
func (s *Store) ForgetKey(device string) error {
	_, err := s.db.Exec(`DELETE FROM keyring WHERE device = ?`, device)
	return err
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func putKey(db execer, device string, public, wrapped []byte) error {
	_, err := db.Exec(`INSERT INTO keyring (device, public, wrapped, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(device) DO UPDATE SET public = excluded.public, wrapped = excluded.wrapped, updated_at = excluded.updated_at`,
		device, public, wrapped, utcNow(), utcNow())
	return err
}

// sealedID tells whether p is the name a sealed brain keeps a file under: an HMAC of its path, in hex.
func sealedID(p string) bool {
	b, err := hex.DecodeString(p)
	return err == nil && len(b) == 32
}

func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}
