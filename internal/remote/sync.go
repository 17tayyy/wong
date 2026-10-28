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
	st, err := LoadState(brain)
	if err != nil {
		return res, err
	}
	changes, cursor, err := c.Changes(ctx, st.Cursor)
	if err != nil {
		return res, err
	}
	for _, ch := range changes {
		if err := ValidPath(ch.Path); err != nil {
			return res, err
		}
		localSha, err := fileSha(filepath.Join(brain, ch.Path))
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
	return res, st.save(brain)
}

// Push sends every file changed since the last sync, except those waiting for a conflict to be resolved.
func Push(ctx context.Context, c *Client, brain string) (Result, error) {
	var res Result
	st, err := LoadState(brain)
	if err != nil {
		return res, err
	}
	local, err := walk(brain)
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
	accepted, conflicts, err := c.Push(ctx, pushes)
	if err != nil {
		return res, err
	}
	sent := map[string]string{}
	for _, p := range pushes {
		sent[p.Path] = p.Sha
	}
	for _, path := range accepted {
		st.synced(path, sent[path])
		res.Pushed = append(res.Pushed, path)
	}
	for _, ch := range conflicts {
		if err := st.conflict(brain, ch); err != nil {
			return res, err
		}
		res.Conflicts = append(res.Conflicts, ch.Path)
	}
	return res, st.save(brain)
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

func LoadState(brain string) (State, error) {
	st := State{Files: map[string]string{}, Conflicts: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(brain, statePath))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", statePath, err)
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
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(brain, statePath), append(data, '\n'), "")
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

func fileSha(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return Sha(data), nil
}

// walk returns the sha of every file in the brain, outside each machine's own folders: git's, wong's and Obsidian's.
func walk(brain string) (map[string]string, error) {
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
		sha, err := fileSha(path)
		if err != nil {
			return err
		}
		files[rel] = sha
		return nil
	})
	return files, err
}
