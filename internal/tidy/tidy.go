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
	"github.com/17tayyy/wong/internal/note"
)

const (
	duplicateThreshold = 0.18
	bodyPrefix         = 600
	minMoveScore       = 3
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
		why how apply name description metadata type kind scope sources verified`) {
		stopwords[w] = true
	}
}

// Note is one note as tidy reads it.
type Note struct {
	Path        string
	Name        string
	Description string
	Kind        note.Kind
	words       map[string]bool
	text        string
	head        string
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
				words:       words(head + " " + body),
				text:        strings.ToLower(n.Body),
				head:        head,
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
			if jaccard(prefs[i].words, prefs[j].words) >= duplicateThreshold {
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

// Misplaced suggests moving notes that name another folder's project far more than their own.
// names maps each project id to the words that identify it, such as its repos' directory names.
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

func words(text string) map[string]bool {
	set := map[string]bool{}
	for _, w := range wordRE.FindAllString(strings.ToLower(text), -1) {
		if !stopwords[w] {
			set[w] = true
		}
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
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
