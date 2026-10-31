package remote

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

const (
	// ReadOnly lets an account read its brain and take it away, but not change it.
	ReadOnly = "read-only"
	// Suspended closes an account to everything but the person reading why.
	Suspended = "suspended"
)

var (
	// ErrReadOnly refuses a change to an account that may only read its brain.
	ErrReadOnly = errors.New("this account is read-only")
	// ErrSuspended refuses anything to a suspended account.
	ErrSuspended = errors.New("this account is suspended")
)

// Standing is what the service that runs the accounts says of one: whether it may write, a sentence for its person
// with where to act on it and until when it holds, and how much it may keep when that differs from the server's limit.
type Standing struct {
	Mode   string `json:"mode,omitempty"`
	Notice string `json:"notice,omitempty"`
	Link   string `json:"link,omitempty"`
	Until  string `json:"until,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// standingCache keeps a store's standing in memory, since every request asks for it.
type standingCache struct {
	mu    sync.Mutex
	known bool
	st    Standing
}

// Standing returns what the service that runs the accounts last said of this one; an account it said nothing of stands
// as any other.
func (s *Store) Standing() (Standing, error) {
	s.standing.mu.Lock()
	defer s.standing.mu.Unlock()
	if s.standing.known {
		return s.standing.st, nil
	}
	var st Standing
	err := s.db.QueryRow(`SELECT mode, notice, link, until, size FROM standing`).Scan(&st.Mode, &st.Notice, &st.Link, &st.Until, &st.Size)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	s.standing.known, s.standing.st = true, st
	return st, nil
}

// SetStanding records what the service that runs the accounts says of this one.
func (s *Store) SetStanding(st Standing) error {
	switch st.Mode {
	case "", ReadOnly, Suspended:
	default:
		return fmt.Errorf("unknown mode %q", st.Mode)
	}
	s.standing.mu.Lock()
	defer s.standing.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO standing (id, mode, notice, link, until, size, updated_at) VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET mode = excluded.mode, notice = excluded.notice, link = excluded.link, until = excluded.until,
			size = excluded.size, updated_at = excluded.updated_at`, st.Mode, st.Notice, st.Link, st.Until, st.Size, utcNow())
	if err == nil {
		s.standing.known, s.standing.st = true, st
	}
	return err
}

// Usable refuses a suspended account, saying why when the service gave a reason.
func (s *Store) Usable() error {
	st, err := s.Standing()
	if err != nil || st.Mode != Suspended {
		return err
	}
	return refuse(ErrSuspended, "%s", st.say("This account is suspended."))
}

// writable refuses a change to an account that is read-only or suspended.
func (s *Store) writable() error {
	st, err := s.Standing()
	switch {
	case err != nil:
		return err
	case st.Mode == ReadOnly:
		return refuse(ErrReadOnly, "%s", st.say("This account is read-only: its brain can be read and taken away, but not changed."))
	case st.Mode == Suspended:
		return refuse(ErrSuspended, "%s", st.say("This account is suspended."))
	}
	return nil
}

// say is the standing's notice, or what to say without one, with where to act on it.
func (st Standing) say(otherwise string) string {
	msg := otherwise
	if st.Notice != "" {
		msg = st.Notice
	}
	if st.Link != "" {
		msg += " " + st.Link
	}
	return msg
}
