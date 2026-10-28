// Package tidy finds notes that repeat each other and notes that seem to live in the wrong folder.
package tidy

import (
	"cmp"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/graph"
	"github.com/17tayyy/wong/internal/note"
)

const (
	duplicateThreshold = 0.18
	bodyPrefix         = 600
	minMoveScore       = 3
)

// Note is one note as tidy reads it.
type Note struct {
	Path        string
	Name        string
	Description string
	Kind        note.Kind
	words       map[string]bool
	text        string
	head        string
	body        string
}

// Move suggests a note that talks more about another folder than about its own.
type Move struct {
	Note, To      string
	Score, Others int
}

// Load reads every note of the brain.
func Load(dir string) ([]Note, error) {
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
		for _, path := range paths {
			if filepath.Base(path) == "MEMORY.md" {
				continue
			}
			n, err := note.Load(path)
			if err != nil {
				return nil, err
			}
			kind := n.Kind
			if kind == "" && (n.ClaudeType() == "feedback" || n.ClaudeType() == "user") {
				kind = note.Preference
			}
			body := n.Body
			if len(body) > bodyPrefix {
				body = body[:bodyPrefix]
			}
			head := strings.ToLower(n.Name + " " + n.Description)
			notes = append(notes, Note{
				Path:        filepath.Join(folder, filepath.Base(path)),
				Name:        n.Name,
				Description: n.Description,
				Kind:        kind,
				words:       graph.Words(head + " " + body),
				text:        strings.ToLower(n.Body),
				head:        head,
				body:        n.Body,
			})
		}
	}
	return notes, nil
}

// Duplicates groups preferences whose words overlap enough to say the same thing.
func Duplicates(notes []Note) [][]string {
	var prefs []Note
	for _, n := range notes {
		if n.Kind == note.Preference {
			prefs = append(prefs, n)
		}
	}
	parent := make([]int, len(prefs))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for i := range prefs {
		for j := i + 1; j < len(prefs); j++ {
			if graph.Jaccard(prefs[i].words, prefs[j].words) >= duplicateThreshold {
				parent[find(i)] = find(j)
			}
		}
	}
	groups := map[int][]string{}
	for i, n := range prefs {
		groups[find(i)] = append(groups[find(i)], n.Path)
	}
	var out [][]string
	for _, g := range groups {
		if len(g) > 1 {
			slices.Sort(g)
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b []string) int { return cmp.Or(cmp.Compare(len(b), len(a)), cmp.Compare(a[0], b[0])) })
	return out
}

// Misplaced suggests moving notes that name another project (by its words in names) far more than their own folder.
func Misplaced(notes []Note, names map[string][]string) []Move {
	patterns := map[string][]*regexp.Regexp{}
	for id, words := range names {
		for _, w := range words {
			patterns[id] = append(patterns[id], regexp.MustCompile(`(^|[^\p{L}\p{N}_-])`+regexp.QuoteMeta(strings.ToLower(w))+`($|[^\p{L}\p{N}_-])`))
		}
	}
	ids := slices.Sorted(maps.Keys(patterns))
	var moves []Move
	for _, n := range notes {
		if n.Kind == note.Preference && filepath.Dir(n.Path) == "me" {
			continue
		}
		own := strings.TrimPrefix(filepath.Dir(n.Path), "projects/")
		ownScore := mentions(n, patterns[own])
		best, bestScore := "", 0
		for _, id := range ids {
			if score := mentions(n, patterns[id]); id != own && score > bestScore {
				best, bestScore = id, score
			}
		}
		if bestScore >= minMoveScore && bestScore > 2*ownScore {
			moves = append(moves, Move{Note: n.Path, To: filepath.Join("projects", best), Score: bestScore, Others: ownScore})
		}
	}
	return moves
}

func mentions(n Note, patterns []*regexp.Regexp) int {
	score := 0
	for _, re := range patterns {
		score += 3*len(re.FindAllStringIndex(n.head, -1)) + len(re.FindAllStringIndex(n.text, -1))
	}
	return score
}

// Growth is a note that grows by dated additions instead of being rewritten with what holds now.
type Growth struct {
	Note         string
	Dated, Parts int
	First, Last  string
	Size         int
}

const minDated = 4

var (
	paragraphRE = regexp.MustCompile(`\n\s*\n`)
	dateRE      = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
)

// Growing lists the notes with minDated or more paragraphs that open with a date, those with the most first.
func Growing(notes []Note) []Growth {
	var out []Growth
	for _, n := range notes {
		g := Growth{Note: n.Path, Size: len(n.body)}
		for _, p := range paragraphRE.Split(n.body, -1) {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			g.Parts++
			if dateRE.MatchString(p[:min(len(p), 160)]) {
				g.Dated++
			}
		}
		if g.Dated < minDated {
			continue
		}
		dates := dateRE.FindAllString(n.body, -1)
		slices.Sort(dates)
		g.First, g.Last = dates[0], dates[len(dates)-1]
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b Growth) int {
		return cmp.Or(cmp.Compare(b.Dated, a.Dated), cmp.Compare(b.Size, a.Size), strings.Compare(a.Note, b.Note))
	})
	return out
}
