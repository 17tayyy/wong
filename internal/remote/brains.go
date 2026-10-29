package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Brains is where a server keeps its brains: one Store, or a Folder with one per account.
type Brains interface {
	// Auth returns the brain a token opens and whose token it is, or a zero Session when it opens none.
	Auth(token string) (Session, error)
	// Visit returns the brain a dashboard session opens, or a zero Session when it opens none.
	Visit(session string) (Session, error)
}

// Session is a token's way into a brain: the brain's store, its account in a Folder, and the token's name.
type Session struct {
	Store   *Store
	Account string
	Machine string
}

// Who names the token for logs: its name, after its account's in a Folder.
func (s Session) Who() string {
	if s.Account == "" {
		return s.Machine
	}
	return s.Account + "/" + s.Machine
}

func (s *Store) Auth(token string) (Session, error) {
	machine, err := s.Machine(token)
	if err != nil || machine == "" {
		return Session{}, err
	}
	return Session{Store: s, Account: s.id, Machine: machine}, nil
}

// ErrNoAccount marks an account the folder does not have.
var ErrNoAccount = errors.New("no such account")

var accountRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidAccount accepts an account id: lowercase letters, digits and hyphens.
func ValidAccount(id string) error {
	if !accountRE.MatchString(id) {
		return fmt.Errorf("invalid account %q: use lowercase letters, digits and hyphens", id)
	}
	return nil
}

// Folder keeps the brain of each account in a SQLite file of its own, to copy, move or delete alone.
// Its tokens carry their account, which keeps them valid when the file is served alone.
type Folder struct {
	dir  string
	mu   sync.Mutex
	open map[string]*Store
}

func OpenFolder(dir string) (*Folder, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Folder{dir: dir, open: map[string]*Store{}}, nil
}

func (f *Folder) Auth(token string) (Session, error) {
	id, ok := accountOf(token)
	if !ok {
		return Session{}, nil
	}
	s, err := f.Store(id)
	if errors.Is(err, ErrNoAccount) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, err
	}
	return s.Auth(token)
}

func (f *Folder) Visit(session string) (Session, error) {
	id, ok := accountOf(session)
	if !ok {
		return Session{}, nil
	}
	s, err := f.Store(id)
	if errors.Is(err, ErrNoAccount) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, err
	}
	return s.Visit(session)
}

// Store opens the brain of an account the folder has.
func (f *Folder) Store(id string) (*Store, error) {
	s, _, err := f.brain(id, false)
	return s, err
}

// Create opens the brain of an account, making it when the folder lacks it, and reports whether it did.
func (f *Folder) Create(id string) (*Store, bool, error) {
	return f.brain(id, true)
}

func (f *Folder) brain(id string, create bool) (*Store, bool, error) {
	if err := ValidAccount(id); err != nil {
		return nil, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.open[id]; ok {
		return s, false, nil
	}
	file := filepath.Join(f.dir, id+".db")
	_, err := os.Stat(file)
	missing := errors.Is(err, fs.ErrNotExist)
	switch {
	case missing && !create:
		return nil, false, fmt.Errorf("%w: %s", ErrNoAccount, id)
	case err != nil && !missing:
		return nil, false, err
	}
	s, err := Open(file)
	if err != nil {
		return nil, false, err
	}
	s.id = id
	f.open[id] = s
	return s, missing, nil
}

// IDs lists the folder's accounts.
func (f *Folder) IDs() ([]string, error) {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".db"); ok && e.Type().IsRegular() && accountRE.MatchString(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (f *Folder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var errs []error
	for id, s := range f.open {
		errs = append(errs, s.Close())
		delete(f.open, id)
	}
	return errors.Join(errs...)
}

// accountOf reads the account of a Folder's token: wong_<account>_<secret>.
func accountOf(token string) (string, bool) {
	rest, ok := strings.CutPrefix(token, "wong_")
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(rest, "_")
	return id, ok && accountRE.MatchString(id)
}
