// Package link points Claude Code's memory at the brain by editing autoMemoryDirectory in its settings.
package link

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	settingsPath = ".claude/settings.local.json"
	memoryKey    = "autoMemoryDirectory"
)

var (
	keyRE           = regexp.MustCompile(`"` + memoryKey + `"[ \t]*:[ \t]*"(?:[^"\\]|\\.)*"`)
	keyLineRE       = regexp.MustCompile(`\n?[ \t]*"` + memoryKey + `"[ \t]*:[ \t]*"(?:[^"\\]|\\.)*"[ \t]*,?`)
	trailingCommaRE = regexp.MustCompile(`,(\s*})`)
)

// MemoryDir is the setting's value for a folder of the brain, written with ~/ when the brain lives under home.
func MemoryDir(brain, home, rel string) string {
	dir := filepath.Join(brain, rel)
	if r, err := filepath.Rel(home, dir); err == nil && r != ".." && !strings.HasPrefix(r, "../") {
		return "~/" + filepath.ToSlash(r)
	}
	return dir
}

// Current returns the memory directory a settings file sets, or "" when it sets none.
func Current(settings string) (string, error) {
	data, err := os.ReadFile(settings)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return "", fmt.Errorf("%s: %w", settings, err)
	}
	var dir string
	if raw, ok := values[memoryKey]; ok {
		if err := json.Unmarshal(raw, &dir); err != nil {
			return "", fmt.Errorf("%s: %s: %w", settings, memoryKey, err)
		}
	}
	return dir, nil
}

// Settings is the local settings file of a repo or folder.
func Settings(dir string) string {
	return filepath.Join(dir, settingsPath)
}

// Link points a repo or folder at memoryDir, keeping its other local settings and making git ignore them.
func Link(dir, memoryDir string) error {
	if err := Set(Settings(dir), memoryDir); err != nil {
		return err
	}
	return ignore(dir)
}

func Unlink(dir string) error {
	return Unset(Settings(dir), true)
}

// Set writes the memory directory into a settings file, touching nothing else in it.
func Set(settings, memoryDir string) error {
	value, err := json.Marshal(memoryDir)
	if err != nil {
		return err
	}
	pair := fmt.Sprintf("%q: %s", memoryKey, value)
	data, err := os.ReadFile(settings)
	var out string
	switch {
	case errors.Is(err, fs.ErrNotExist) || strings.TrimSpace(string(data)) == "" || isEmptyObject(data):
		out = "{\n  " + pair + "\n}\n"
	case err != nil:
		return err
	case keyRE.Match(data):
		out = keyRE.ReplaceAllLiteralString(string(data), pair)
	default:
		i := strings.Index(string(data), "{")
		if i < 0 {
			return fmt.Errorf("%s: not a JSON object", settings)
		}
		out = string(data[:i+1]) + "\n  " + pair + "," + string(data[i+1:])
	}
	if got, err := currentOf([]byte(out)); err != nil || got != memoryDir {
		return fmt.Errorf("%s: could not set %s without breaking the file", settings, memoryKey)
	}
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		return err
	}
	return os.WriteFile(settings, []byte(out), 0o644)
}

// Unset removes the memory directory from a settings file; removeEmpty deletes a file left with nothing else.
func Unset(settings string, removeEmpty bool) error {
	data, err := os.ReadFile(settings)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !keyRE.Match(data) {
		return nil
	}
	out := keyLineRE.ReplaceAllString(string(data), "")
	if !json.Valid([]byte(out)) {
		out = trailingCommaRE.ReplaceAllString(out, "$1")
	}
	if got, err := currentOf([]byte(out)); err != nil || got != "" {
		return fmt.Errorf("%s: could not remove %s without breaking the file", settings, memoryKey)
	}
	if removeEmpty && isEmptyObject([]byte(out)) {
		return os.Remove(settings)
	}
	return os.WriteFile(settings, []byte(out), 0o644)
}

func currentOf(data []byte) (string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return "", err
	}
	var dir string
	if raw, ok := values[memoryKey]; ok {
		if err := json.Unmarshal(raw, &dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func isEmptyObject(data []byte) bool {
	var values map[string]json.RawMessage
	return json.Unmarshal(data, &values) == nil && len(values) == 0
}

func ignore(dir string) error {
	if exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run() != nil {
		return nil
	}
	err := exec.Command("git", "-C", dir, "check-ignore", "-q", settingsPath).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &exit) || exit.ExitCode() != 1:
		return fmt.Errorf("git check-ignore in %s: %w", dir, err)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return fmt.Errorf("git rev-parse in %s: %w", dir, err)
	}
	exclude := strings.TrimSpace(string(out))
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(dir, exclude)
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
