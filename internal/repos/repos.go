// Package repos keeps this machine's local checkouts of each project.
package repos

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Registry maps a project id to the directories of its local repos.
type Registry map[string][]string

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wong", "repos.yaml"), nil
}

func Load(path string) (Registry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Registry{}, nil
	}
	if err != nil {
		return nil, err
	}
	r := Registry{}
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

func (r Registry) Add(project, dir string) {
	if !slices.Contains(r[project], dir) {
		r[project] = append(r[project], dir)
		slices.Sort(r[project])
	}
}

func (r Registry) ProjectOf(dir string) string {
	for id, dirs := range r {
		if slices.Contains(dirs, dir) {
			return id
		}
	}
	return ""
}

// InMain returns path as it lies in the main checkout when it is inside a linked git worktree, and path itself otherwise.
func InMain(path string) string {
	path = filepath.Clean(path)
	for dir := path; ; {
		if info, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			main := ""
			if !info.IsDir() {
				main = mainCheckout(filepath.Join(dir, ".git"))
			}
			if main == "" {
				return path
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return path
			}
			return filepath.Join(main, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		dir = parent
	}
}

// mainCheckout reads the .git file of a linked worktree and returns the folder of its main checkout, or "" for anything else.
func mainCheckout(gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(gitFile), gitdir)
	}
	main, _, ok := strings.Cut(filepath.ToSlash(filepath.Clean(gitdir)), "/.git/worktrees/")
	if !ok {
		return ""
	}
	return filepath.FromSlash(main)
}

// Containing returns the project and folder of the registered folder that holds path, the deepest when several do.
func (r Registry) Containing(path string) (project, dir string) {
	for id, dirs := range r {
		for _, d := range dirs {
			if strings.HasPrefix(path, d+string(filepath.Separator)) && len(d) > len(dir) {
				project, dir = id, d
			}
		}
	}
	return project, dir
}

func (r Registry) Save(path string) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(map[string][]string(r)); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
