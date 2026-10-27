package check

import (
	"regexp"
	"slices"
	"strings"
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
	for _, m := range linkRE.FindAllStringSubmatch(body, -1) {
		if link := strings.TrimSpace(m[1]); !slices.Contains(r.links, link) {
			r.links = append(r.links, link)
		}
	}
	return r
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
