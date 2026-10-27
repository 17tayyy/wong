// Package skills records in the brain where the user's skills come from, and copies the ones that have no source.
package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const lockName = "skills-lock.json"

// Lock is the skills-lock.json format of the skills installer (npx skills), which can restore from it.
type Lock struct {
	Version int              `json:"version"`
	Skills  map[string]Entry `json:"skills"`
}

type Entry struct {
	Source     string `json:"source"`
	SourceType string `json:"sourceType"`
	SourceURL  string `json:"sourceUrl,omitempty"`
	SkillPath  string `json:"skillPath,omitempty"`
}

type Env struct {
	Brain string
	Home  string
	Repos map[string][]string
}

// Result lists what Save recorded: skills by their source and copies, as paths in the brain.
type Result struct {
	Sourced []string
	Copied  []string
}

// Save adds the global skills' sources to the ones the brain records from every machine, and copies every skill that has none and does not travel with its repo.
func Save(env Env) (Result, error) {
	var res Result
	local, err := readLock(filepath.Join(env.Home, ".agents", ".skill-lock.json"))
	if err != nil {
		return res, err
	}
	path := filepath.Join(env.Brain, "skills", lockName)
	global, err := readLock(path)
	if err != nil {
		return res, err
	}
	maps.Copy(global.Skills, local.Skills)
	if err := writeLock(path, global); err != nil {
		return res, err
	}
	for _, name := range slices.Sorted(maps.Keys(global.Skills)) {
		res.Sourced = append(res.Sourced, name)
	}
	for _, dir := range []string{filepath.Join(env.Home, ".agents", "skills"), filepath.Join(env.Home, ".claude", "skills")} {
		copied, err := copyUnsourced(dir, filepath.Join(env.Brain, "skills"), global, nil)
		if err != nil {
			return res, err
		}
		res.Copied = append(res.Copied, rel(env.Brain, copied)...)
	}

	for _, id := range slices.Sorted(maps.Keys(env.Repos)) {
		for _, repo := range env.Repos[id] {
			local, err := readLock(filepath.Join(repo, lockName))
			if err != nil {
				return res, err
			}
			dest := filepath.Join(env.Brain, "skills", "projects", id, filepath.Base(repo))
			if len(local.Skills) > 0 && !tracked(repo, lockName) {
				if err := writeLock(filepath.Join(dest, lockName), local); err != nil {
					return res, err
				}
				for _, name := range slices.Sorted(maps.Keys(local.Skills)) {
					res.Sourced = append(res.Sourced, filepath.Join(id, filepath.Base(repo), name))
				}
			}
			for _, sub := range []string{".claude/skills", ".agents/skills"} {
				copied, err := copyUnsourced(filepath.Join(repo, sub), dest, local, func(name string) bool {
					return tracked(repo, filepath.Join(sub, name))
				})
				if err != nil {
					return res, err
				}
				res.Copied = append(res.Copied, rel(env.Brain, copied)...)
			}
		}
	}
	return res, nil
}

// copyUnsourced mirrors into dest each skill of dir that the lock does not name and skip does not exclude.
func copyUnsourced(dir, dest string, lock Lock, skip func(string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var copied []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "synced" {
			continue
		}
		if _, ok := lock.Skills[name]; ok || skip != nil && skip(name) {
			continue
		}
		src := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
			continue
		}
		if err := mirror(src, filepath.Join(dest, name)); err != nil {
			return nil, err
		}
		copied = append(copied, filepath.Join(dest, name))
	}
	return copied, nil
}

// mirror makes dst a copy of src, following src itself when it is a symlink, and leaves unchanged files untouched.
func mirror(src, dst string) error {
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	err = filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		r, _ := filepath.Rel(real, path)
		want[r] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, r)
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if r, _ := filepath.Rel(dst, path); !want[r] {
			return os.Remove(path)
		}
		return nil
	})
}

func readLock(path string) (Lock, error) {
	lock := Lock{Version: 1, Skills: map[string]Entry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return lock, nil
	}
	if err != nil {
		return lock, err
	}
	var raw struct {
		Skills map[string]Entry `json:"skills"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return lock, fmt.Errorf("%s: %w", path, err)
	}
	if raw.Skills != nil {
		lock.Skills = raw.Skills
	}
	return lock, nil
}

func writeLock(path string, lock Lock) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) || err != nil && len(lock.Skills) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func tracked(repo, path string) bool {
	out, err := exec.Command("git", "-C", repo, "ls-files", "--", path).Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

func rel(base string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		r, _ := filepath.Rel(base, p)
		out = append(out, r)
	}
	return out
}
