// Package brain finds, writes and moves notes in the brain, keeping each folder's MEMORY.md in step.
package brain

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/note"
)

const indexName = "MEMORY.md"

var (
	fileStemRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	slugRE     = regexp.MustCompile(`[^a-z0-9._-]+`)
	anyLinkRE  = regexp.MustCompile(`\]\([^)]*\.md\)`)
)

// Folder returns the brain folder and scope for a target: "me" or a project id.
func Folder(target string) (folder, scope string, err error) {
	if target == "me" {
		return "me", "global", nil
	}
	scope, err = note.ProjectScope(target)
	if err != nil {
		return "", "", err
	}
	return filepath.Join("projects", target), scope, nil
}

// Resolve finds a note by its path (in the brain or absolute), its name or its file name without .md.
func Resolve(brain, ref string) (string, error) {
	if strings.HasSuffix(ref, ".md") {
		rel := ref
		if filepath.IsAbs(ref) {
			r, err := filepath.Rel(brain, ref)
			if err != nil || strings.HasPrefix(r, "..") {
				return "", fmt.Errorf("%s is not in the brain (%s)", ref, brain)
			}
			rel = r
		}
		if _, err := os.Stat(filepath.Join(brain, rel)); err != nil {
			return "", fmt.Errorf("no note %s in %s", rel, brain)
		}
		return filepath.Clean(rel), nil
	}
	folders, err := Folders(brain)
	if err != nil {
		return "", err
	}
	var found []string
	for _, folder := range folders {
		paths, err := notesIn(filepath.Join(brain, folder))
		if err != nil {
			return "", err
		}
		for _, path := range paths {
			rel := filepath.Join(folder, filepath.Base(path))
			if strings.TrimSuffix(filepath.Base(path), ".md") == ref {
				found = append(found, rel)
				continue
			}
			if n, err := note.Load(path); err == nil && n.Name == ref {
				found = append(found, rel)
			}
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no note named %q in %s", ref, brain)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("%q matches several notes, give the path: %s", ref, strings.Join(found, ", "))
	}
}

// Folders lists the note folders of the brain: me, then projects/<id>.
func Folders(brain string) ([]string, error) {
	projects, err := filepath.Glob(filepath.Join(brain, "projects", "*"))
	if err != nil {
		return nil, err
	}
	folders := []string{"me"}
	for _, p := range projects {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			folders = append(folders, filepath.Join("projects", filepath.Base(p)))
		}
	}
	return folders, nil
}

// Write creates a note in a folder and adds it to the folder's MEMORY.md.
func Write(brain, folder string, n *note.Note) (string, error) {
	stem, err := Stem(n.Name)
	if err != nil {
		return "", err
	}
	rel := filepath.Join(folder, stem+".md")
	path := filepath.Join(brain, rel)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists: edit it and run wong note verify", rel)
	}
	n.Alias(stem)
	data, err := n.Marshal()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return rel, addToIndex(filepath.Join(brain, folder), filepath.Base(rel), n)
}

// Stem returns the file name, without .md, for a note with this name.
func Stem(name string) (string, error) {
	stem := name
	if !fileStemRE.MatchString(stem) {
		stem = strings.Trim(slugRE.ReplaceAllString(strings.ToLower(stem), "-"), "-._")
	}
	if stem == "" {
		return "", fmt.Errorf("cannot make a file name from %q", name)
	}
	return stem, nil
}

// Move puts a note in another folder, updating its scope and both folders' MEMORY.md.
func Move(brain, rel, target string) (string, error) {
	folder, scope, err := Folder(target)
	if err != nil {
		return "", err
	}
	file := filepath.Base(rel)
	dest := filepath.Join(folder, file)
	if dest == filepath.Clean(rel) {
		return "", fmt.Errorf("%s is already in %s", rel, folder)
	}
	if _, err := os.Stat(filepath.Join(brain, dest)); err == nil {
		return "", fmt.Errorf("%s already exists", dest)
	}
	src := filepath.Join(brain, rel)
	n, err := note.Load(src)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	n.Scope = scope
	data, err := n.Marshal()
	if err != nil {
		return "", err
	}
	path := filepath.Join(brain, dest)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		return "", err
	}
	if err := os.Remove(src); err != nil {
		return "", err
	}
	if err := removeFromIndex(filepath.Dir(src), file); err != nil {
		return "", err
	}
	return dest, addToIndex(filepath.Join(brain, folder), file, n)
}

// Remove deletes a note and its link in MEMORY.md, and returns the notes that link to it.
func Remove(brain, rel string) ([]string, error) {
	path := filepath.Join(brain, rel)
	n, err := note.Load(path)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	if err := removeFromIndex(filepath.Dir(path), filepath.Base(rel)); err != nil {
		return nil, err
	}
	return linkersOf(brain, n.Name, strings.TrimSuffix(filepath.Base(rel), ".md"))
}

func linkersOf(brain string, names ...string) ([]string, error) {
	folders, err := Folders(brain)
	if err != nil {
		return nil, err
	}
	var linkers []string
	for _, folder := range folders {
		paths, err := notesIn(filepath.Join(brain, folder))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if slices.ContainsFunc(names, func(name string) bool { return strings.Contains(string(data), "[["+name+"]]") }) {
				linkers = append(linkers, filepath.Join(folder, filepath.Base(path)))
			}
		}
	}
	return linkers, nil
}

func addToIndex(dir, file string, n *note.Note) error {
	path := filepath.Join(dir, indexName)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := WithIndexEntry(string(data), file, n)
	if text == string(data) {
		return nil
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// WithIndexEntry returns a MEMORY.md with a line for the note's file, unless it already links to it.
func WithIndexEntry(index, file string, n *note.Note) string {
	if strings.Contains(index, "("+file+")") {
		return index
	}
	if index != "" && !strings.HasSuffix(index, "\n") {
		index += "\n"
	}
	return index + fmt.Sprintf("- [%s](%s) — %s\n", n.Name, file, strings.Join(strings.Fields(n.Description), " "))
}

// removeFromIndex drops a note's link from MEMORY.md, and the whole line when no other link is left on it.
func removeFromIndex(dir, file string) error {
	path := filepath.Join(dir, indexName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(WithoutIndexEntry(string(data), file)), 0o644)
}

// WithoutIndexEntry returns a MEMORY.md without the link to file, and without its line when no other link is left on it.
func WithoutIndexEntry(index, file string) string {
	link := `\[[^\]]*\]\(` + regexp.QuoteMeta(file) + `\)`
	linkRE := regexp.MustCompile(link)
	withSeparatorRE := regexp.MustCompile(`\s*·\s*` + link + `|` + link + `\s*·\s*`)
	lines := strings.SplitAfter(index, "\n")
	lines = slices.DeleteFunc(lines, func(line string) bool {
		return linkRE.MatchString(line) && !anyLinkRE.MatchString(linkRE.ReplaceAllString(line, ""))
	})
	for i, line := range lines {
		if linkRE.MatchString(line) {
			lines[i] = withSeparatorRE.ReplaceAllString(line, "")
		}
	}
	return strings.Join(lines, "")
}

// WithIndexLine returns a MEMORY.md whose link to file points to newFile under the note's name, adding a line when there is none.
func WithIndexLine(index, file, newFile string, n *note.Note) string {
	link := `\[[^\]]*\]\(` + regexp.QuoteMeta(file) + `\)`
	label := fmt.Sprintf("[%s](%s)", n.Name, newFile)
	linkRE := regexp.MustCompile(link)
	if !linkRE.MatchString(index) {
		return WithIndexEntry(index, newFile, n)
	}
	whole := regexp.MustCompile(`(?m)^(\s*[-*+] )` + link + `(?: — .*)?$`)
	index = whole.ReplaceAllStringFunc(index, func(line string) string {
		return whole.FindStringSubmatch(line)[1] + label + " — " + strings.Join(strings.Fields(n.Description), " ")
	})
	return linkRE.ReplaceAllLiteralString(index, label)
}

func notesIn(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(paths, func(p string) bool { return filepath.Base(p) == indexName }), nil
}
