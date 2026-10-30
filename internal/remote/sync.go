package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	statePath    = ".wong/sync.json"
	conflictsDir = ".wong/conflicts"
)

// State is what this machine last synced: the server's cursor, each file's sha, and the conflicts left to resolve.
type State struct {
	Cursor    int64             `json:"cursor"`
	Files     map[string]string `json:"files"`
	Conflicts map[string]string `json:"conflicts,omitempty"`
}

type Result struct {
	Updated   []string
	Deleted   []string
	Pushed    []string
	Conflicts []string
}

// Pull applies the server's changes to the files this machine has not changed since its last sync.
func Pull(ctx context.Context, c *Client, brain string) (Result, error) {
	var res Result
	if err := c.Unlock(ctx); err != nil {
		return res, err
	}
	st, err := loadState(brain, c.statePath())
	if err != nil {
		return res, err
	}
	changes, cursor, err := c.Changes(ctx, st.Cursor)
	if err != nil {
		return res, err
	}
	known, err := c.known(brain, st)
	if err != nil {
		return res, err
	}
	for _, ch := range changes {
		ch, ok, err := c.unseal(ch, known)
		if err != nil {
			return res, err
		}
		if !ok {
			continue
		}
		if err := ValidPath(ch.Path); err != nil {
			return res, err
		}
		localSha, err := c.fileSum(filepath.Join(brain, ch.Path))
		if err != nil {
			return res, err
		}
		_, waiting := st.Conflicts[ch.Path]
		switch {
		case localSha == ch.Sha:
			st.synced(ch.Path, ch.Sha)
		case ch.Sha == st.Files[ch.Path] && !waiting:
		case localSha == st.Files[ch.Path]:
			if err := apply(brain, ch); err != nil {
				return res, err
			}
			st.synced(ch.Path, ch.Sha)
			if ch.Deleted {
				res.Deleted = append(res.Deleted, ch.Path)
			} else {
				res.Updated = append(res.Updated, ch.Path)
			}
		default:
			if err := st.conflict(brain, ch); err != nil {
				return res, err
			}
			res.Conflicts = append(res.Conflicts, ch.Path)
		}
	}
	st.Cursor = cursor
	return res, st.saveAt(brain, c.statePath())
}

// Push sends every file changed since the last sync, except those waiting for a conflict to be resolved.
func Push(ctx context.Context, c *Client, brain string) (Result, error) {
	var res Result
	if err := c.Unlock(ctx); err != nil {
		return res, err
	}
	st, err := loadState(brain, c.statePath())
	if err != nil {
		return res, err
	}
	local, err := walk(brain, c.sum)
	if err != nil {
		return res, err
	}
	var pushes []Upload
	for path, sha := range local {
		if _, waiting := st.Conflicts[path]; waiting || sha == st.Files[path] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(brain, path))
		if err != nil {
			return res, err
		}
		info, err := os.Stat(filepath.Join(brain, path))
		if err != nil {
			return res, err
		}
		pushes = append(pushes, Upload{Path: path, Base: st.Files[path], Sha: sha, Content: data, Modified: info.ModTime().UTC().Format(time.RFC3339Nano)})
	}
	for path, synced := range st.Files {
		if _, ok := local[path]; ok {
			continue
		}
		if _, waiting := st.Conflicts[path]; !waiting {
			pushes = append(pushes, Upload{Path: path, Base: synced})
		}
	}
	if len(pushes) == 0 {
		return res, nil
	}
	slices.SortFunc(pushes, func(a, b Upload) int { return strings.Compare(a.Path, b.Path) })
	sealed, known, err := c.seal(pushes)
	if err != nil {
		return res, err
	}
	accepted, conflicts, err := c.Push(ctx, sealed)
	if err != nil {
		return res, err
	}
	sent := map[string]string{}
	for _, p := range pushes {
		sent[p.Path] = p.Sha
	}
	for _, name := range accepted {
		path, ok := known[name]
		if !ok {
			continue
		}
		st.synced(path, sent[path])
		res.Pushed = append(res.Pushed, path)
	}
	for _, ch := range conflicts {
		ch, ok, err := c.unseal(ch, known)
		if err != nil {
			return res, err
		}
		if !ok {
			continue
		}
		if err := st.conflict(brain, ch); err != nil {
			return res, err
		}
		res.Conflicts = append(res.Conflicts, ch.Path)
	}
	return res, st.saveAt(brain, c.statePath())
}

// Resolve ends a conflict keeping this machine's version (pushed next time) or the server's.
func Resolve(brain, path string, keepLocal bool) error {
	st, err := LoadState(brain)
	if err != nil {
		return err
	}
	serverSha, ok := st.Conflicts[path]
	if !ok {
		return fmt.Errorf("%s has no conflict", path)
	}
	copyPath := filepath.Join(brain, conflictsDir, path)
	if !keepLocal {
		target := filepath.Join(brain, path)
		if serverSha == "" {
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		} else {
			data, err := os.ReadFile(copyPath)
			if err != nil {
				return err
			}
			if err := writeFile(target, data, ""); err != nil {
				return err
			}
		}
	}
	st.Files[path] = serverSha
	if serverSha == "" {
		delete(st.Files, path)
	}
	delete(st.Conflicts, path)
	if err := os.Remove(copyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return st.save(brain)
}

// LoadState reads what this machine last synced with its server.
func LoadState(brain string) (State, error) {
	return loadState(brain, statePath)
}

func loadState(brain, path string) (State, error) {
	st := State{Files: map[string]string{}, Conflicts: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(brain, path))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", path, err)
	}
	if st.Files == nil {
		st.Files = map[string]string{}
	}
	if st.Conflicts == nil {
		st.Conflicts = map[string]string{}
	}
	return st, nil
}

func (st *State) synced(path, sha string) {
	if sha == "" {
		delete(st.Files, path)
	} else {
		st.Files[path] = sha
	}
	delete(st.Conflicts, path)
}

// conflict keeps the server's version of a file under .wong/conflicts, leaving this machine's untouched.
func (st *State) conflict(brain string, ch Change) error {
	st.Conflicts[ch.Path] = ch.Sha
	copyPath := filepath.Join(brain, conflictsDir, ch.Path)
	if ch.Deleted || ch.Sha == "" {
		if err := os.Remove(copyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFile(copyPath, ch.Content, ch.Modified)
}

func (st *State) save(brain string) error {
	return st.saveAt(brain, statePath)
}

func (st *State) saveAt(brain, path string) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(brain, path), append(data, '\n'), "")
}

// statePath is where the client keeps what it last synced: State when set, for a brain that syncs with more than one
// server, or the brain's own sync state.
func (c *Client) statePath() string {
	if c.State != "" {
		return c.State
	}
	return statePath
}

func apply(brain string, ch Change) error {
	target := filepath.Join(brain, ch.Path)
	if ch.Deleted || ch.Sha == "" {
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		dir := filepath.Dir(target)
		for dir != filepath.Clean(brain) && os.Remove(dir) == nil {
			dir = filepath.Dir(dir)
		}
		return nil
	}
	return writeFile(target, ch.Content, ch.Modified)
}

func writeFile(path string, data []byte, modified string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339Nano, modified); err == nil {
		return os.Chtimes(path, t, t)
	}
	return nil
}

// sum is the sha a file syncs with: the sha256 of its content, or for a sealed brain the brain key's HMAC of it.
func (c *Client) sum(data []byte) string {
	if c.Key != nil {
		return c.Key.Sum(data)
	}
	return Sha(data)
}

func (c *Client) fileSum(path string) (string, error) {
	return fileSum(path, c.sum)
}

func fileSum(path string, sum func([]byte) string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sum(data), nil
}

// seal turns the uploads of a sealed brain into blobs under their ids, and returns the path each name stands for; a
// brain in the clear goes as it is.
func (c *Client) seal(pushes []Upload) ([]Upload, map[string]string, error) {
	known := map[string]string{}
	if c.Key == nil {
		for _, p := range pushes {
			known[p.Path] = p.Path
		}
		return pushes, known, nil
	}
	sealed := make([]Upload, 0, len(pushes))
	for _, p := range pushes {
		u := Upload{Path: c.Key.ID(p.Path), Base: p.Base, Sha: p.Sha, Modified: p.Modified}
		if p.Sha != "" {
			var err error
			if _, u.Content, err = c.Key.Seal(p.Path, p.Content); err != nil {
				return nil, nil, err
			}
		}
		known[u.Path] = p.Path
		sealed = append(sealed, u)
	}
	return sealed, known, nil
}

// known maps the ids of a sealed brain to the paths this machine has or last synced, for the deletions the server
// reports without content to decrypt.
func (c *Client) known(brain string, st State) (map[string]string, error) {
	known := map[string]string{}
	if c.Key == nil {
		return known, nil
	}
	local, err := walk(brain, c.sum)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for path := range local {
		known[c.Key.ID(path)] = path
	}
	for path := range st.Files {
		known[c.Key.ID(path)] = path
	}
	for path := range st.Conflicts {
		known[c.Key.ID(path)] = path
	}
	return known, nil
}

// unseal turns a change of a sealed brain back into its path and content; a deletion of a file this machine never had
// is skipped. A blob that does not open, because it was changed or does not belong to this brain, is an error.
func (c *Client) unseal(ch Change, known map[string]string) (Change, bool, error) {
	if c.Key == nil {
		return ch, true, nil
	}
	if ch.Deleted || ch.Sha == "" {
		path, ok := known[ch.Path]
		ch.Path = path
		return ch, ok, nil
	}
	path, content, err := c.Key.Open(ch.Path, ch.Content)
	if err != nil {
		return ch, false, fmt.Errorf("a file from the server does not decrypt with this brain's key: %w", err)
	}
	known[ch.Path] = path
	ch.Path, ch.Content = path, content
	return ch, true, nil
}

// walk returns the sum of every file in the brain, outside each machine's own folders: git's, wong's and Obsidian's.
func walk(brain string, sum func([]byte) string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(brain, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(brain, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == ".wong" || rel == ".obsidian" || rel == ".trash" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		sha, err := fileSum(path, sum)
		if err != nil {
			return err
		}
		files[rel] = sha
		return nil
	})
	return files, err
}
