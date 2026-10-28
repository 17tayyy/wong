package check

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/note"
)

var (
	backtickRE   = regexp.MustCompile("`([^`\n]+)`")
	fileExtRE    = regexp.MustCompile(`(^|/)[^/.][^/]*\.(py|pyi|go|ts|tsx|js|jsx|mjs|cjs|json|ya?ml|toml|md|mdx|html|css|scss|sql|sh|lua|rs|java|kt|rb|php|vue|svelte|txt|ini|cfg|proto|graphql|tf)$`)
	hostRE       = regexp.MustCompile(`^[\w-]+(\.[\w-]+)+/`)
	fileAnchorRE = regexp.MustCompile(`(:\d+)+$|#.*$`)
	commitRE     = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	issueRE      = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)
	linkRE       = regexp.MustCompile(`\[\[([^\]|#]+)`)
	clauseRE     = regexp.MustCompile(`[.;·|](\s|$)|\s—\s|\n`)
	openWordRE   = regexp.MustCompile(`(?i)\b(en pausa|pausad[oa]|pendiente|por hacer|sin empezar|no empezar|bloquead[oa]|en curso|en progreso|sin desplegar|sin mergear|sin commitear|in progress|paused|pending|blocked|not started|on hold|backlog|wip)\b`)
)

type refs struct {
	unknown []string
	files   []string
	commits []string
	issues  []string
	// clauses holds, per issue, the text that talks about it: up to the next issue in the same clause.
	clauses map[string][]string
	links   []string
}

func extract(body string) refs {
	r := refs{clauses: map[string][]string{}}
	for _, m := range backtickRE.FindAllStringSubmatch(body, -1) {
		if f, ok := fileRef(m[1]); ok && !slices.Contains(r.files, f) {
			r.files = append(r.files, f)
		}
	}
	for _, loc := range commitRE.FindAllStringIndex(body, -1) {
		if sha := body[loc[0]:loc[1]]; isCommit(body, loc) && !slices.Contains(r.commits, sha) {
			r.commits = append(r.commits, sha)
		}
	}
	for _, clause := range clauseRE.Split(body, -1) {
		locs := issueRE.FindAllStringIndex(clause, -1)
		for i, loc := range locs {
			id := clause[loc[0]:loc[1]]
			start, end := loc[0], len(clause)
			if i == 0 {
				start = 0
			}
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			if _, seen := r.clauses[id]; !seen {
				r.issues = append(r.issues, id)
			}
			r.clauses[id] = append(r.clauses[id], clause[start:end])
		}
	}
	for _, m := range linkRE.FindAllStringSubmatch(note.WithoutCode(body), -1) {
		if link := strings.TrimSpace(m[1]); !slices.Contains(r.links, link) && !note.IsAttachment(link) {
			r.links = append(r.links, link)
		}
	}
	return r
}

// addSources adds what a note declares in sources: linear:<ID>, file:<repo>/<path>, commit:<repo>@<sha>, note:<name> or a URL.
func (r *refs) addSources(sources []string) {
	for _, s := range sources {
		kind, value, _ := strings.Cut(s, ":")
		switch {
		case kind == "linear" && issueRE.FindString(value) == value:
			if _, seen := r.clauses[value]; !seen {
				r.issues = append(r.issues, value)
				r.clauses[value] = nil
			}
		case kind == "file" && strings.Contains(value, "/"):
			if !slices.Contains(r.files, value) {
				r.files = append(r.files, value)
			}
		case kind == "commit" && sourceCommit(value) != "":
			if sha := sourceCommit(value); !slices.Contains(r.commits, sha) {
				r.commits = append(r.commits, sha)
			}
		case kind == "note" && value != "":
			if !slices.Contains(r.links, value) {
				r.links = append(r.links, value)
			}
		case kind == "http" || kind == "https":
		default:
			r.unknown = append(r.unknown, s)
		}
	}
}

// ValidSource reports whether check understands a source.
func ValidSource(s string) bool {
	r := refs{clauses: map[string][]string{}}
	r.addSources([]string{s})
	return len(r.unknown) == 0
}

func sourceCommit(value string) string {
	_, sha, ok := strings.Cut(value, "@")
	if !ok || commitRE.FindString(sha) != sha || len(sha) > 12 && len(sha) != 40 {
		return ""
	}
	return sha
}

func fileRef(token string) (string, bool) {
	t := fileAnchorRE.ReplaceAllString(strings.TrimSpace(token), "")
	if strings.ContainsAny(t, " *<>{}\\$") || strings.Contains(t, "...") || strings.Contains(t, "://") || hostRE.MatchString(t) || !fileExtRE.MatchString(t) {
		return "", false
	}
	return strings.TrimPrefix(t, "./"), true
}

func isCommit(body string, loc []int) bool {
	sha := body[loc[0]:loc[1]]
	if len(sha) > 12 && len(sha) != 40 {
		return false
	}
	if !strings.ContainsAny(sha, "abcdef") || !strings.ContainsAny(sha, "0123456789") {
		return false
	}
	before, after := loc[0]-1, loc[1]
	if before >= 0 && strings.ContainsRune("-.", rune(body[before])) {
		return false
	}
	if after < len(body) && body[after] == '-' {
		return false
	}
	return after+1 >= len(body) || body[after] != '.' || !isAlnum(body[after+1])
}

func isAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func openWord(clauses []string) string {
	for _, c := range clauses {
		if w := openWordRE.FindString(c); w != "" {
			return w
		}
	}
	return ""
}

// References returns the issues, files and commits a note cites in its text and its sources.
func References(n *note.Note) []string {
	r := extract(n.Description + "\n" + n.Body)
	r.addSources(n.Sources)
	return slices.Concat(r.issues, r.files, r.commits)
}

// Undeclared lists, as sources, what a note's text cites and its sources leave out: issue keys, and the files and commits found in dirs.
func Undeclared(n *note.Note, dirs []string, home string) []string {
	cited := extract(n.Description + "\n" + n.Body)
	declared := refs{clauses: map[string][]string{}}
	declared.addSources(n.Sources)
	var out []string
	for _, id := range cited.issues {
		if !slices.Contains(declared.issues, id) {
			out = append(out, "linear:"+id)
		}
	}
	for _, f := range cited.files {
		if s, ok := fileSource(f, dirs, home); ok && !slices.Contains(declared.files, strings.TrimPrefix(s, "file:")) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, sha := range cited.commits {
		if slices.ContainsFunc(declared.commits, func(d string) bool { return strings.HasPrefix(d, sha) || strings.HasPrefix(sha, d) }) {
			continue
		}
		for _, dir := range dirs {
			if _, err := git(dir, "", "cat-file", "-e", sha+"^{commit}"); err == nil {
				out = append(out, "commit:"+filepath.Base(dir)+"@"+sha)
				break
			}
		}
	}
	return out
}

// fileSource names a cited file as a file: source of the repo among dirs that holds it.
func fileSource(f string, dirs []string, home string) (string, bool) {
	if rest, ok := strings.CutPrefix(f, "~/"); ok {
		f = filepath.Join(home, rest)
	}
	for _, dir := range dirs {
		rel, ok := (&repo{dir: dir}).rel(f)
		if !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			return "file:" + filepath.Base(dir) + "/" + filepath.ToSlash(rel), true
		}
	}
	return "", false
}
