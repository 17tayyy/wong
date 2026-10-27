// Package importer moves Claude Code's per-folder memory into the brain.
package importer

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/note"
)

const (
	indexName     = "MEMORY.md"
	indexMaxLines = 200
)

var (
	claudeUnsafeRE = regexp.MustCompile(`[^a-zA-Z0-9]`)
	idUnsafeRE     = regexp.MustCompile(`[^a-z0-9._-]+`)
	titleRE        = regexp.MustCompile(`\A# [^\n]*\n`)
	headingRE      = regexp.MustCompile(`(?m)^#+ `)
)

var kindOf = map[string]note.Kind{
	"user":      note.Preference,
	"feedback":  note.Preference,
	"project":   note.Project,
	"reference": note.Reference,
}

// Source is one Claude Code memory folder and where its notes go in the brain.
type Source struct {
	Folder string
	Dir    string
	Path   string
	Repo   string
	Target string
	Scope  string
	Notes  []string
}

func (s Source) Label() string {
	if s.Path == "" {
		return s.Folder
	}
	return filepath.Base(s.Path)
}

type Result struct {
	Written   int
	Unchanged int
	Warnings  []string
}

// Plan maps every memory folder under projectsDir to a brain target; overrides go by folder or path.
func Plan(projectsDir, home string, overrides map[string]string) ([]Source, error) {
	dirs, err := filepath.Glob(filepath.Join(projectsDir, "*", "memory"))
	if err != nil {
		return nil, err
	}
	var sources []Source
	for _, dir := range dirs {
		notes, err := notesIn(dir)
		if err != nil {
			return nil, err
		}
		if len(notes) == 0 {
			continue
		}
		folder := filepath.Base(filepath.Dir(dir))
		path := resolve("/", folder)
		var repo string
		if path != "" {
			repo = gitToplevel(path)
		}
		id, ok := overrides[folder]
		if !ok && path != "" {
			id, ok = overrides[path]
		}
		if !ok {
			if path == "" {
				return nil, fmt.Errorf("%s: no directory on disk matches this memory folder; assign it with --map '%s=ID'", folder, folder)
			}
			id = project(path, repo, home)
		}
		target, scope, err := place(id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", folder, err)
		}
		sources = append(sources, Source{Folder: folder, Dir: dir, Path: path, Repo: repo, Target: target, Scope: scope, Notes: notes})
	}
	return sources, nil
}

// Apply writes notes, keeping their modification times, and merged indexes into brain, never overwriting changes.
func Apply(brain string, sources []Source, dryRun bool) (Result, error) {
	var res Result
	files := map[string][]byte{}
	origin := map[string]string{}
	var order, problems []string
	for _, group := range byTarget(sources) {
		target := group[0].Target
		var entries []entry
		for _, s := range group {
			for _, path := range s.Notes {
				n, err := note.Load(path)
				if err != nil {
					return res, err
				}
				if n.Kind == "" {
					n.Kind = kindOf[n.ClaudeType()]
				}
				if n.Scope == "" {
					n.Scope = s.Scope
				}
				data, err := n.Marshal()
				if err != nil {
					return res, fmt.Errorf("%s: %w", path, err)
				}
				file := filepath.Base(path)
				rel := filepath.Join(target, file)
				if prev, ok := files[rel]; ok {
					if !bytes.Equal(prev, data) {
						problems = append(problems, rel+": two memory folders have different notes with this file name")
					}
					continue
				}
				files[rel] = data
				origin[rel] = path
				order = append(order, rel)
				entries = append(entries, entry{file: file, note: n})
			}
		}
		index, err := mergeIndex(group, entries)
		if err != nil {
			return res, err
		}
		rel := filepath.Join(target, indexName)
		files[rel] = index
		order = append(order, rel)
		if lines := bytes.Count(index, []byte("\n")); lines > indexMaxLines {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s has %d lines; Claude Code only loads the first %d", rel, lines, indexMaxLines))
		}
	}

	var pending []string
	for _, rel := range order {
		existing, err := os.ReadFile(filepath.Join(brain, rel))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			pending = append(pending, rel)
		case err != nil:
			return res, err
		case bytes.Equal(existing, files[rel]):
			if filepath.Base(rel) != indexName {
				res.Unchanged++
			}
		default:
			problems = append(problems, rel+": already in the brain with different content")
		}
	}
	if len(problems) > 0 {
		return res, fmt.Errorf("nothing imported:\n  %s", strings.Join(problems, "\n  "))
	}
	for _, rel := range pending {
		if filepath.Base(rel) != indexName {
			res.Written++
		}
		if dryRun {
			continue
		}
		path := filepath.Join(brain, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(path, files[rel], 0o644); err != nil {
			return res, err
		}
	}
	if dryRun {
		return res, nil
	}
	for rel, src := range origin {
		info, err := os.Stat(src)
		if err != nil {
			return res, err
		}
		if err := os.Chtimes(filepath.Join(brain, rel), info.ModTime(), info.ModTime()); err != nil {
			return res, err
		}
	}
	return res, nil
}

type entry struct {
	file string
	note *note.Note
}

func notesIn(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(paths, func(p string) bool { return filepath.Base(p) == indexName }), nil
}

func resolve(dir, folder string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		encoded := claudeUnsafeRE.ReplaceAllString(path, "-")
		if encoded != folder && !strings.HasPrefix(folder, encoded+"-") {
			continue
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			continue
		}
		if encoded == folder {
			return path
		}
		if found := resolve(path, folder); found != "" {
			return found
		}
	}
	return ""
}

func project(path, repo, home string) string {
	if path == home {
		return "me"
	}
	base := path
	if repo != "" {
		base = repo
		if parent := filepath.Dir(repo); parent != home && parent != "/" {
			base = parent
		}
	}
	return strings.Trim(idUnsafeRE.ReplaceAllString(strings.ToLower(filepath.Base(base)), "-"), "-._")
}

func gitToplevel(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func place(id string) (target, scope string, err error) {
	if id == "me" {
		return "me", "global", nil
	}
	scope, err = note.ProjectScope(id)
	if err != nil {
		return "", "", err
	}
	return "projects/" + id, scope, nil
}

func byTarget(sources []Source) [][]Source {
	var groups [][]Source
	at := map[string]int{}
	for _, s := range sources {
		i, ok := at[s.Target]
		if !ok {
			i = len(groups)
			at[s.Target] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], s)
	}
	for _, g := range groups {
		slices.SortStableFunc(g, func(a, b Source) int { return len(b.Notes) - len(a.Notes) })
	}
	return groups
}

func mergeIndex(group []Source, entries []entry) ([]byte, error) {
	var b strings.Builder
	if len(group) > 1 {
		b.WriteString("# Memory Index\n")
	}
	for _, s := range group {
		data, err := os.ReadFile(filepath.Join(s.Dir, indexName))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(group) == 1 {
			b.Write(data)
			continue
		}
		body := strings.TrimSpace(titleRE.ReplaceAllString(string(data), ""))
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", s.Label(), headingRE.ReplaceAllString(body, "#$0"))
	}
	text := b.String()
	var missing []string
	for _, e := range entries {
		if !strings.Contains(text, "("+e.file+")") {
			missing = append(missing, fmt.Sprintf("- [%s](%s) — %s", e.note.Name, e.file, strings.Join(strings.Fields(e.note.Description), " ")))
		}
	}
	if len(missing) > 0 {
		if text == "" {
			text = "# Memory Index\n"
		}
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n" + strings.Join(missing, "\n") + "\n"
	}
	return []byte(text), nil
}
