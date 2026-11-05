package remote

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Link ties an account of the server to what an integration sends webhooks for: an installation of the GitHub App, by
// its id, or a Linear workspace, by its organization id. Account is "" on a server with a single brain.
type Link struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Account string `json:"account"`
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	Linked  string `json:"linked"`
}

// Links keeps the server's links in a file, so an app's webhooks reach the brains they are for.
type Links struct {
	mu   sync.Mutex
	file string
	list []Link
}

// OpenLinks reads the links kept in file, which need not exist yet.
func OpenLinks(file string) (*Links, error) {
	l := &Links{file: file}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &l.list); err != nil {
		return nil, err
	}
	return l, nil
}

// Add keeps a link, in place of the one it repeats.
func (l *Links) Add(link Link) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if link.Linked == "" {
		link.Linked = time.Now().UTC().Format(time.RFC3339)
	}
	list := slices.DeleteFunc(slices.Clone(l.list), func(o Link) bool {
		return o.Kind == link.Kind && o.ID == link.ID && o.Account == link.Account
	})
	return l.save(append(list, link))
}

// Remove drops an account's link to an installation or a workspace.
func (l *Links) Remove(kind, id, account string) error {
	return l.drop(func(o Link) bool { return o.Kind == kind && o.ID == id && o.Account == account })
}

// Forget drops every link to an installation or a workspace, once it is gone.
func (l *Links) Forget(kind, id string) error {
	return l.drop(func(o Link) bool { return o.Kind == kind && o.ID == id })
}

// DropAccount drops every link of an account the server no longer keeps.
func (l *Links) DropAccount(account string) error {
	return l.drop(func(o Link) bool { return o.Account == account })
}

// Accounts returns the accounts linked to an installation or a workspace.
func (l *Links) Accounts(kind, id string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, o := range l.list {
		if o.Kind == kind && o.ID == id {
			out = append(out, o.Account)
		}
	}
	return out
}

// Of returns an account's links of one kind.
func (l *Links) Of(kind, account string) []Link {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Link
	for _, o := range l.list {
		if o.Kind == kind && o.Account == account {
			out = append(out, o)
		}
	}
	return out
}

func (l *Links) drop(match func(Link) bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.ContainsFunc(l.list, match) {
		return nil
	}
	return l.save(slices.DeleteFunc(slices.Clone(l.list), match))
}

func (l *Links) save(list []Link) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.file), ".links-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), l.file); err != nil {
		return err
	}
	l.list = list
	return nil
}
