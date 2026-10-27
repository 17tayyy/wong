// Package link points a repo's Claude Code memory at the brain.
package link

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	settingsPath = ".claude/settings.local.json"
	memoryKey    = "autoMemoryDirectory"
)

// MemoryDir is the setting's value for a project, written with ~/ when the brain lives under home.
func MemoryDir(brain, home, project string) string {
	dir := filepath.Join(brain, "projects", project)
	if rel, err := filepath.Rel(home, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return "~/" + filepath.ToSlash(rel)
	}
	return dir
}

// Current returns the memory directory a repo is linked to, or "" when it is not linked.
func Current(repo string) (string, error) {
	settings, err := read(repo)
	if err != nil {
		return "", err
	}
	var dir string
	if raw, ok := settings[memoryKey]; ok {
		if err := json.Unmarshal(raw, &dir); err != nil {
			return "", fmt.Errorf("%s: %s: %w", filepath.Join(repo, settingsPath), memoryKey, err)
		}
	}
	return dir, nil
}

func Link(repo, memoryDir string) error {
	settings, err := read(repo)
	if err != nil {
		return err
	}
	value, err := json.Marshal(memoryDir)
	if err != nil {
		return err
	}
	settings[memoryKey] = value
	if err := write(repo, settings); err != nil {
		return err
	}
	return ignore(repo)
}

func Unlink(repo string) error {
	settings, err := read(repo)
	if err != nil {
		return err
	}
	if _, ok := settings[memoryKey]; !ok {
		return nil
	}
	delete(settings, memoryKey)
	if len(settings) == 0 {
		return os.Remove(filepath.Join(repo, settingsPath))
	}
	return write(repo, settings)
}

func read(repo string) (map[string]json.RawMessage, error) {
	path := filepath.Join(repo, settingsPath)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return settings, nil
}

func write(repo string, settings map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(repo, settingsPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func ignore(repo string) error {
	err := exec.Command("git", "-C", repo, "check-ignore", "-q", settingsPath).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &exit) || exit.ExitCode() != 1:
		return fmt.Errorf("git check-ignore in %s: %w", repo, err)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return fmt.Errorf("git rev-parse in %s: %w", repo, err)
	}
	exclude := strings.TrimSpace(string(out))
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(repo, exclude)
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	current, err := os.ReadFile(exclude)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	line := "/" + settingsPath + "\n"
	if len(current) > 0 && current[len(current)-1] != '\n' {
		line = "\n" + line
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
