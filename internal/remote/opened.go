package remote

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/17tayyy/wong/internal/seal"
)

// Opened is a sealed brain decrypted into a store in memory and kept in step with where the sealed brain lives: the
// server's own store, when the server opens it for the AIs that run in the cloud, or the server through a client, in a
// browser that holds the key. Refresh brings in what the machines pushed; Flush seals back what changed in memory.
type Opened struct {
	Plain *Store
	key   *seal.Key
	side  sealedSide
	mu    sync.Mutex
	// paths maps each sealed id to its path; sums and shas hold, by path, the sum on the sealed side and the sha in
	// memory of the version both sides agree on.
	paths   map[string]string
	sums    map[string]string
	shas    map[string]string
	cursor  int64
	flushed int64
}

// sealedSide is where a sealed brain lives.
type sealedSide interface {
	Changes(ctx context.Context, since int64) ([]Change, int64, error)
	Apply(ctx context.Context, machine string, ups []Upload) ([]string, []Change, error)
}

type storeSide struct{ s *Store }

func (s storeSide) Changes(_ context.Context, since int64) ([]Change, int64, error) {
	return s.s.Changes(since)
}

func (s storeSide) Apply(_ context.Context, machine string, ups []Upload) ([]string, []Change, error) {
	return s.s.Apply(machine, ups)
}

// clientSide reaches the sealed brain through a client, whose token signs what it pushes.
type clientSide struct{ c *Client }

func (s clientSide) Changes(ctx context.Context, since int64) ([]Change, int64, error) {
	return s.c.Changes(ctx, since)
}

func (s clientSide) Apply(ctx context.Context, _ string, ups []Upload) ([]string, []Change, error) {
	return s.c.Push(ctx, ups)
}

// OpenThrough decrypts the sealed brain c reaches, with the key c unlocked, into a store in memory.
func OpenThrough(ctx context.Context, c *Client) (*Opened, error) {
	if err := c.Unlock(ctx); err != nil {
		return nil, err
	}
	if c.Key == nil {
		return nil, errors.New("the brain is not end-to-end encrypted")
	}
	o, err := open(c.Key, clientSide{c})
	if err != nil {
		return nil, err
	}
	if err := o.Refresh(ctx); err != nil {
		o.Close()
		return nil, err
	}
	return o, nil
}

func open(key *seal.Key, side sealedSide) (*Opened, error) {
	plain, err := Open(":memory:")
	if err != nil {
		return nil, err
	}
	return &Opened{Plain: plain, key: key, side: side, paths: map[string]string{}, sums: map[string]string{}, shas: map[string]string{}}, nil
}

// Close drops the brain from memory.
func (o *Opened) Close() error {
	return o.Plain.Close()
}

// Refresh brings into memory what was pushed to the sealed side since the last time. A note changed in memory and
// not flushed yet that the sealed side changed too is an error, and stays as it is in memory.
func (o *Opened) Refresh(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	changes, cursor, err := o.side.Changes(ctx, o.cursor)
	if err != nil {
		return err
	}
	before, err := o.Plain.cursor()
	if err != nil {
		return err
	}
	var ups []Upload
	sums := map[string]string{}
	for _, ch := range changes {
		path, sum := o.paths[ch.Path], ""
		var content []byte
		if !ch.Deleted && ch.Sha != "" {
			if path, content, err = o.key.Open(ch.Path, ch.Content); err != nil {
				return fmt.Errorf("a file of the brain does not decrypt with its key: %w", err)
			}
			o.paths[ch.Path], sum = path, ch.Sha
		}
		if path == "" || o.sums[path] == sum {
			continue
		}
		up := Upload{Path: path, Base: o.shas[path], Content: content, Modified: ch.Modified}
		if sum != "" {
			up.Sha = Sha(content)
		}
		ups = append(ups, up)
		sums[path] = sum
	}
	var clash []string
	if len(ups) > 0 {
		accepted, conflicts, err := o.Plain.Apply("sync", ups)
		if err != nil {
			return err
		}
		for _, up := range ups {
			if slices.Contains(accepted, up.Path) {
				o.sums[up.Path], o.shas[up.Path] = sums[up.Path], up.Sha
			}
		}
		for _, c := range conflicts {
			clash = append(clash, c.Path)
		}
	}
	o.cursor = cursor
	if before == o.flushed {
		if o.flushed, err = o.Plain.cursor(); err != nil {
			return err
		}
	}
	if len(clash) > 0 {
		return &ClashError{Paths: clash}
	}
	return nil
}

// ClashError names the notes changed both in memory and on another machine: the memory keeps its version, the sealed
// side keeps the other, and neither overwrites the other.
type ClashError struct{ Paths []string }

func (e *ClashError) Error() string {
	return strings.Join(e.Paths, ", ") + " changed both here and on another machine; reopen the brain to take the other version"
}

// Flush seals what changed in memory since the last flush into the sealed side, signed by machine where the sealed
// side is the server's store. A note the sealed side changed meanwhile is not overwritten: it is an error.
func (o *Opened) Flush(ctx context.Context, machine string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	changes, cursor, err := o.Plain.Changes(o.flushed)
	if err != nil {
		return err
	}
	var ups []Upload
	sha := map[string]string{}
	for _, ch := range changes {
		if ch.Deleted {
			ch.Sha = ""
		}
		if ch.Sha == o.shas[ch.Path] {
			continue
		}
		up := Upload{Path: o.key.ID(ch.Path), Base: o.sums[ch.Path], Modified: ch.Modified}
		if ch.Sha != "" {
			up.Sha = o.key.Sum(ch.Content)
			if _, up.Content, err = o.key.Seal(ch.Path, ch.Content); err != nil {
				return err
			}
		}
		ups = append(ups, up)
		o.paths[up.Path], sha[up.Path] = ch.Path, ch.Sha
	}
	if len(ups) == 0 {
		o.flushed = cursor
		return nil
	}
	accepted, conflicts, err := o.side.Apply(ctx, machine, ups)
	if err != nil {
		return err
	}
	sums := map[string]string{}
	for _, up := range ups {
		sums[up.Path] = up.Sha
	}
	for _, id := range accepted {
		path := o.paths[id]
		o.sums[path], o.shas[path] = sums[id], sha[id]
	}
	if len(conflicts) > 0 {
		clash := make([]string, 0, len(conflicts))
		for _, c := range conflicts {
			clash = append(clash, o.paths[c.Path])
		}
		return &ClashError{Paths: clash}
	}
	o.flushed = cursor
	return nil
}
