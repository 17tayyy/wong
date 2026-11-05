package note

import (
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// generic holds folder names too common to say which part of a repo a note is about.
var generic = map[string]bool{}

func init() {
	for _, name := range strings.Fields(`app apps src lib libs test tests docs doc internal cmd pkg scripts packages
		frontend backend web api public static assets config configs bin build dist vendor node_modules`) {
		generic[name] = true
	}
}

var (
	backtickPathRE = regexp.MustCompile("`([^`\\s]*/[^`\\s]*)`")
	fileSourceRE   = regexp.MustCompile(`(?m)^\s*-\s*file:(\S+)`)
	lineAnchorRE   = regexp.MustCompile(`(:\d+)+$|#.*$`)
)

// Reach is how closely a note reaches a file of a repo, rel being its path there and dir the repo's folder on this
// machine, "" when there is none: 2 when the note names the file, 1 when it names a folder holding it or applies to
// it, and 0 when it does neither.
func Reach(data []byte, rel, repo, dir, home string) int {
	best := 0
	for _, ref := range cited(string(data)) {
		best = max(best, reaches(ref, rel, repo, dir, home))
	}
	if best > 0 {
		return best
	}
	n, err := Parse(data)
	if err == nil && slices.ContainsFunc(n.AppliesTo, func(g string) bool {
		return MatchGlob(g, rel) || MatchGlob(g, repo+"/"+rel)
	}) {
		return 1
	}
	return 0
}

// cited lists the paths a note names: in backticks with a slash, and in its file: sources.
func cited(text string) []string {
	var out []string
	for _, m := range backtickPathRE.FindAllStringSubmatch(text, -1) {
		if !strings.Contains(m[1], "://") {
			out = append(out, m[1])
		}
	}
	for _, m := range fileSourceRE.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// reaches is 2 when a cited path names the file, 1 when it names a folder holding it, and 0 when it names too little.
func reaches(ref, rel, repo, dir, home string) int {
	folder := strings.HasSuffix(ref, "/")
	ref = strings.TrimSuffix(strings.TrimPrefix(lineAnchorRE.ReplaceAllString(ref, ""), "./"), "/")
	if rest, ok := strings.CutPrefix(ref, "~/"); ok {
		ref = filepath.ToSlash(filepath.Join(home, rest))
	}
	if path.IsAbs(ref) {
		if dir == "" {
			return 0
		}
		inside, ok := strings.CutPrefix(ref, filepath.ToSlash(dir)+"/")
		if !ok {
			return 0
		}
		ref = inside
	}
	if strings.ContainsAny(ref, "*?") {
		return 0
	}
	if !strings.Contains(ref, "/") {
		if folder && !generic[ref] && strings.HasPrefix(rel, ref+"/") {
			return 1
		}
		return 0
	}
	full := repo + "/" + rel
	switch {
	case ref == rel, ref == full, strings.HasSuffix(rel, "/"+ref):
		return 2
	case strings.HasPrefix(rel, ref+"/"), strings.HasPrefix(full, ref+"/"):
		return 1
	}
	return 0
}
