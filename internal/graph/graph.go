// Package graph sees the brain as notes joined by links and tags, and keeps agents from fraying it.
package graph

import (
	"cmp"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/note"
)

// Note is one note of the brain and where it lives.
type Note struct {
	Path string
	*note.Note
}

// Shelf is me or the project id the note lives on.
func (n Note) Shelf() string {
	if id, ok := strings.CutPrefix(path.Dir(n.Path), "projects/"); ok {
		return id
	}
	return path.Dir(n.Path)
}

func (n Note) stem() string { return strings.TrimSuffix(path.Base(n.Path), ".md") }

// Graph is every note of the brain, the names that reach each one and the tags in use.
type Graph struct {
	Notes  []Note
	byPath map[string]Note
	names  map[string]string
	tags   map[string]int
}

func New(notes []Note) *Graph {
	g := &Graph{byPath: map[string]Note{}, names: map[string]string{}, tags: map[string]int{}}
	slices.SortFunc(notes, func(a, b Note) int { return strings.Compare(a.Path, b.Path) })
	g.Notes = notes
	for _, n := range notes {
		g.byPath[n.Path] = n
		g.names[n.Name] = n.Path
		g.names[n.stem()] = n.Path
		for _, t := range n.Tags {
			g.tags[strings.ToLower(t)]++
		}
	}
	return g
}

// Resolve finds the note a [[link]] names: its name or its file name.
func (g *Graph) Resolve(name string) (string, bool) {
	p, ok := g.names[strings.TrimSpace(name)]
	return p, ok
}

// Tags returns every tag in use with how many notes carry it.
func (g *Graph) Tags() map[string]int {
	return maps.Clone(g.tags)
}

// TagList lists the tags in use, most used first, as "tag (n)".
func (g *Graph) TagList(limit int) []string {
	tags := slices.SortedFunc(maps.Keys(g.tags), func(a, b string) int { return cmp.Or(cmp.Compare(g.tags[b], g.tags[a]), strings.Compare(a, b)) })
	if limit > 0 && len(tags) > limit {
		tags = tags[:limit]
	}
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = fmt.Sprintf("%s (%d)", t, g.tags[t])
	}
	return out
}

// CheckTags normalizes a note's tags and refuses a new one that is a spelling of a tag in use or of another one given.
func (g *Graph) CheckTags(tags []string) ([]string, error) {
	var out []string
	for _, t := range tags {
		norm, err := note.NormalizeTag(t)
		if err != nil {
			return nil, err
		}
		if slices.Contains(out, norm) {
			continue
		}
		if g.tags[norm] == 0 {
			for _, used := range slices.Sorted(maps.Keys(g.tags)) {
				if note.CloseTags(norm, used) {
					return nil, fmt.Errorf("tag %q is a spelling of %q, which %d notes use: use %q", norm, used, g.tags[used], used)
				}
			}
			for _, other := range out {
				if note.CloseTags(norm, other) {
					return nil, fmt.Errorf("tags %q and %q are spellings of one tag: keep one", other, norm)
				}
			}
		}
		out = append(out, norm)
	}
	return out, nil
}

// Missing lists the [[links]] in text that reach no note, each with the closest names, leaving out those in before.
func (g *Graph) Missing(text, before string) []string {
	known := map[string]bool{}
	for _, m := range note.WikiLinkRE.FindAllStringSubmatch(before, -1) {
		known[strings.TrimSpace(m[1])] = true
	}
	var out []string
	for _, m := range note.WikiLinkRE.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		if _, ok := g.Resolve(name); ok || known[name] || note.IsAttachment(name) {
			continue
		}
		known[name] = true
		msg := "[[" + name + "]] reaches no note"
		if near := g.nearNames(name, 3); len(near) > 0 {
			msg += "; did you mean " + strings.Join(near, ", ") + "?"
		}
		out = append(out, msg)
	}
	return out
}

func (g *Graph) nearNames(name string, limit int) []string {
	type scored struct {
		name string
		d    int
	}
	key := strings.ToLower(name)
	var all []scored
	for n := range g.names {
		lower := strings.ToLower(n)
		d := note.Distance(key, lower)
		if strings.Contains(lower, key) || strings.Contains(key, lower) {
			d = min(d, 2)
		}
		if d <= max(2, len(key)/3) {
			all = append(all, scored{n, d})
		}
	}
	slices.SortFunc(all, func(a, b scored) int { return cmp.Or(cmp.Compare(a.d, b.d), strings.Compare(a.name, b.name)) })
	var out []string
	for _, s := range all {
		if len(out) == limit {
			break
		}
		out = append(out, "[["+s.name+"]]")
	}
	return out
}

const (
	headThreshold = 0.25
	textThreshold = 0.2
	bodyPrefix    = 600
)

var wordRE = regexp.MustCompile(`[\p{L}\p{N}_-]{3,}`)

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for with are not use always never when before after from this that
		into its it's than then them they there their have has had was were will would should could about
		over only also just like more most what which who how why all any each other such some
		los las del por para con sin que una uno unos unas como más pero sus este esta esto estos estas
		ese esa eso hay ser son fue era está están todo todos toda todas cuando donde sobre entre tras
		ya muy sí también porque desde hasta antes después siempre nunca nada algo
		why how apply name description metadata type kind scope sources verified
		tracked see decided note notes`) {
		stopwords[w] = true
	}
}

// Words returns the distinct words of a text worth comparing: three letters or more, without stopwords.
func Words(text string) map[string]bool {
	set := map[string]bool{}
	for _, w := range wordRE.FindAllString(strings.ToLower(text), -1) {
		if !stopwords[w] {
			set[w] = true
		}
	}
	return set
}

// Jaccard is the share of words two sets have in common.
func Jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}

func head(n Note) map[string]bool { return Words(n.Name + " " + n.Description) }

var issueRE = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)

// text is the words of a note's name, description and start, without links and issue keys, which tie notes but not subjects.
func text(n Note) map[string]bool {
	body := n.Body
	if len(body) > bodyPrefix {
		body = body[:bodyPrefix]
	}
	body = issueRE.ReplaceAllString(note.WikiLinkRE.ReplaceAllString(body, ""), "")
	return Words(n.Name + " " + n.Description + " " + body)
}

// Similar returns the notes on the same shelf, or in me, that look like the same subject as n.
func (g *Graph) Similar(n Note) []Note {
	h, t := head(n), text(n)
	var out []Note
	for _, o := range g.Notes {
		if o.Path == n.Path || (o.Shelf() != n.Shelf() && o.Shelf() != "me") {
			continue
		}
		if strings.EqualFold(o.Name, n.Name) || o.stem() == n.stem() || Jaccard(h, head(o)) >= headThreshold || Jaccard(t, text(o)) >= textThreshold {
			out = append(out, o)
		}
	}
	return out
}

// Related returns notes that share sources, issues, files or tags with n and that it does not link to yet, best first.
func (g *Graph) Related(n Note, limit int) []string {
	linked := map[string]bool{n.Path: true}
	for _, m := range note.WikiLinkRE.FindAllStringSubmatch(n.Body+n.Description, -1) {
		if p, ok := g.Resolve(m[1]); ok {
			linked[p] = true
		}
	}
	refs := check.References(n.Note)
	type scored struct {
		path, why string
		score     int
	}
	var all []scored
	for _, o := range g.Notes {
		if linked[o.Path] {
			continue
		}
		score, why := 0, []string{}
		for _, r := range check.References(o.Note) {
			if slices.Contains(refs, r) {
				score += 2
				why = append(why, r)
			}
		}
		for _, t := range o.Tags {
			if slices.ContainsFunc(n.Tags, func(x string) bool { return strings.EqualFold(x, t) }) {
				score++
				why = append(why, "#"+t)
			}
		}
		if score > 0 {
			all = append(all, scored{o.Path, strings.Join(why, ", "), score})
		}
	}
	slices.SortFunc(all, func(a, b scored) int { return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.path, b.path)) })
	var out []string
	for _, s := range all {
		if len(out) == limit {
			break
		}
		out = append(out, fmt.Sprintf("[[%s]] (%s, shares %s)", g.byPath[s.path].Name, s.path, s.why))
	}
	return out
}

// Load reads the notes of a brain folder on disk; notes whose frontmatter does not parse are left out.
func Load(dir string) (*Graph, error) {
	folders, err := brain.Folders(dir)
	if err != nil {
		return nil, err
	}
	var notes []Note
	for _, folder := range folders {
		paths, err := filepath.Glob(filepath.Join(dir, folder, "*.md"))
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			if filepath.Base(p) == "MEMORY.md" {
				continue
			}
			if n, err := note.Load(p); err == nil {
				notes = append(notes, Note{Path: filepath.ToSlash(filepath.Join(folder, filepath.Base(p))), Note: n})
			}
		}
	}
	return New(notes), nil
}
