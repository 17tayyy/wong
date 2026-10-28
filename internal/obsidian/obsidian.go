// Package obsidian copies the brain into an Obsidian vault and brings a vault's notes into the brain.
package obsidian

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/graph"
	"github.com/17tayyy/wong/internal/note"
)

// ExportReport says what an export wrote.
type ExportReport struct {
	Notes, Indexes, Links int
}

// Export copies the brain's notes and MEMORY.md indexes into an empty folder as a vault, with links Obsidian resolves.
func Export(brainDir, dir string) (ExportReport, error) {
	var rep ExportReport
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return rep, fmt.Errorf("%s is not empty: export into a new or empty folder", dir)
	}
	g, err := graph.Load(brainDir)
	if err != nil {
		return rep, err
	}
	folders, err := brain.Folders(brainDir)
	if err != nil {
		return rep, err
	}
	for _, folder := range folders {
		paths, err := filepath.Glob(filepath.Join(brainDir, folder, "*.md"))
		if err != nil {
			return rep, err
		}
		for _, src := range paths {
			data, err := os.ReadFile(src)
			if err != nil {
				return rep, err
			}
			if filepath.Base(src) == "MEMORY.md" {
				rep.Indexes++
			} else {
				rep.Notes++
				var n int
				data, n = relink(data, g)
				rep.Links += n
			}
			dst := filepath.Join(dir, folder, filepath.Base(src))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return rep, err
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// relink rewrites the [[links]] that reach a note by its name rather than its file name to [[file|text]].
func relink(data []byte, g *graph.Graph) ([]byte, int) {
	count := 0
	out := []byte(note.OutsideCode(string(data), func(text string) string {
		return string(note.WikiLinkRE.ReplaceAllFunc([]byte(text), func(m []byte) []byte {
			sub := note.WikiLinkRE.FindSubmatchIndex(m)
			name := strings.TrimSpace(string(m[sub[2]:sub[3]]))
			p, ok := g.Resolve(name)
			stem := strings.TrimSuffix(path.Base(p), ".md")
			if !ok || stem == name {
				return m
			}
			count++
			label := name
			if sub[4] >= 0 {
				label = string(m[sub[4]:sub[5]])
			}
			rest := string(m[sub[3] : len(m)-2])
			if i := strings.Index(rest, "|"); i >= 0 {
				rest = rest[:i]
			}
			return []byte("[[" + stem + rest + "|" + label + "]]")
		}))
	}))
	return out, count
}

// Imported is a note of the vault and what became of it.
type Imported struct {
	From, To string
	Skipped  string
}

// ImportReport says what an import did, or would do in a dry run.
type ImportReport struct {
	Notes       []Imported
	Attachments []string
	Missing     []string
	IndexLines  int
}

var (
	frontmatterRE = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)
	fenceRE       = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")
	inlineTagRE   = regexp.MustCompile(`(?m)(?:^|\s)#([\p{L}\p{N}_/-]*\p{L}[\p{L}\p{N}_/-]*)`)
	markupRE      = regexp.MustCompile(`^[#>*\-+\s]+|\[\[([^\]|]*\|)?|\]\]|[*_` + "`" + `]`)
)

// Import brings a vault's notes onto one shelf of the brain, keeping their properties and never writing over a note.
func Import(vault, brainDir, shelf string, kind note.Kind, dryRun bool) (ImportReport, error) {
	var rep ImportReport
	folder, scope, err := brain.Folder(shelf)
	if err != nil {
		return rep, err
	}
	g, err := graph.Load(brainDir)
	if err != nil {
		return rep, err
	}
	var notes []*note.Note
	taken := map[string]string{}
	err = filepath.WalkDir(vault, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(vault, p)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			rep.Attachments = append(rep.Attachments, filepath.ToSlash(rel))
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		item := Imported{From: filepath.ToSlash(rel)}
		n, err := fromVault(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())), data, kind, scope)
		if err != nil {
			item.Skipped = err.Error()
			rep.Notes = append(rep.Notes, item)
			return nil
		}
		stem, err := brain.Stem(n.Name)
		if err != nil {
			item.Skipped = "no letters or digits in its name"
			rep.Notes = append(rep.Notes, item)
			return nil
		}
		item.To = filepath.ToSlash(filepath.Join(folder, stem+".md"))
		if _, err := os.Stat(filepath.Join(brainDir, item.To)); err == nil {
			item.Skipped = "the brain already has " + item.To
		} else if other, dup := taken[item.To]; dup {
			item.Skipped = "same file name as " + other
		} else {
			taken[item.To] = item.From
			notes = append(notes, n)
		}
		rep.Notes = append(rep.Notes, item)
		return nil
	})
	if err != nil {
		return rep, err
	}
	all := slices.Clone(g.Notes)
	for i, n := range notes {
		all = append(all, graph.Note{Path: fmt.Sprintf("%s/%d.md", folder, i), Note: n})
	}
	known := graph.New(all)
	for _, n := range notes {
		rep.Missing = append(rep.Missing, known.Missing(n.Description+"\n"+n.Body, "")...)
	}
	slices.Sort(rep.Missing)
	rep.Missing = slices.Compact(rep.Missing)
	if index, err := os.ReadFile(filepath.Join(brainDir, folder, "MEMORY.md")); err == nil {
		rep.IndexLines = strings.Count(string(index), "\n")
	}
	rep.IndexLines += len(notes)
	if dryRun {
		return rep, nil
	}
	for _, n := range notes {
		if _, err := brain.Write(brainDir, folder, n); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// fromVault turns a vault file into a note named after the file, with its description and tags from its text if needed.
func fromVault(name string, data []byte, kind note.Kind, scope string) (*note.Note, error) {
	body := string(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))
	root := &yaml.Node{Kind: yaml.MappingNode}
	if m := frontmatterRE.FindStringSubmatchIndex(body); m != nil {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(body[m[2]:m[3]]), &doc); err != nil {
			return nil, fmt.Errorf("properties do not parse: %v", err)
		}
		if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
			root = doc.Content[0]
		}
		body = body[m[1]:]
	}
	n := note.New(name, "", kind, scope, body)
	var tags []string
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "name", "scope", "verified", "sources":
		case "description":
			if value.Kind == yaml.ScalarNode {
				n.Description = strings.Join(strings.Fields(value.Value), " ")
			}
		case "kind":
			if k, err := note.ParseKind(value.Value); err == nil {
				n.Kind = k
			}
		case "tags", "tag":
			tags = append(tags, scalars(value)...)
		default:
			n.Extra = append(n.Extra, key, value)
		}
	}
	for _, m := range inlineTagRE.FindAllStringSubmatch(fenceRE.ReplaceAllString(body, ""), -1) {
		tags = append(tags, m[1])
	}
	for _, t := range tags {
		if norm, err := note.NormalizeTag(t); err == nil && !slices.Contains(n.Tags, norm) {
			n.Tags = append(n.Tags, norm)
		}
	}
	if n.Description == "" {
		n.Description = firstLine(body, name)
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("empty note")
	}
	if !strings.HasPrefix(n.Body, "\n") {
		n.Body = "\n" + n.Body
	}
	return n, nil
}

func scalars(v *yaml.Node) []string {
	if v.Kind == yaml.ScalarNode {
		return strings.FieldsFunc(v.Value, func(r rune) bool { return r == ',' || r == ' ' })
	}
	var out []string
	for _, c := range v.Content {
		if c.Kind == yaml.ScalarNode {
			out = append(out, c.Value)
		}
	}
	return out
}

// firstLine is the first line of a body with text in it, without markup, as a one-line description.
func firstLine(body, fallback string) string {
	for _, line := range strings.Split(fenceRE.ReplaceAllString(body, ""), "\n") {
		text := strings.Join(strings.Fields(markupRE.ReplaceAllString(strings.TrimSpace(line), "")), " ")
		if text == "" {
			continue
		}
		if r := []rune(text); len(r) > 120 {
			text = strings.TrimSpace(string(r[:119])) + "…"
		}
		return text
	}
	return fallback
}
