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
