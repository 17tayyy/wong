package hook

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/note"
)

const maxFileNotes = 5

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
	sessionRE      = regexp.MustCompile(`^[\w-]{1,128}$`)
)

// FileNotes answers PreToolUse on a file with the notes of its project that cite it or its folder, each once per session.
func FileNotes(in Input, env Env) string {
	file := filepath.Clean(in.ToolInput.FilePath)
	if in.ToolInput.FilePath == "" || !filepath.IsAbs(file) || strings.HasPrefix(file, filepath.Clean(env.Brain)+string(filepath.Separator)) {
		return ""
	}
	project, dir := env.Repos.Containing(file)
	if project == "" {
		return ""
	}
	rel, err := filepath.Rel(dir, file)
	if err != nil {
		return ""
	}
	rel, repo := filepath.ToSlash(rel), filepath.Base(dir)
	type hit struct {
		path, name, desc string
		score            int
	}
	var hits []hit
	notes, _ := filepath.Glob(filepath.Join(env.Brain, "projects", project, "*.md"))
	for _, p := range notes {
		if filepath.Base(p) == "MEMORY.md" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		best := 0
		for _, ref := range cited(string(data)) {
			best = max(best, reaches(ref, rel, repo, dir, env.Home))
		}
		if best == 0 {
			continue
		}
		h := hit{path: p, name: strings.TrimSuffix(filepath.Base(p), ".md"), score: best}
		if n, err := note.Parse(data); err == nil {
			h.name, h.desc = n.Name, n.Description
		}
		hits = append(hits, h)
	}
	if len(hits) == 0 {
		return ""
	}
	shown := seen(env.Brain, in.SessionID)
	hits = slices.DeleteFunc(hits, func(h hit) bool { return shown[h.path] })
	if len(hits) == 0 {
		return ""
	}
	slices.SortFunc(hits, func(a, b hit) int { return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.name, b.name)) })
	if len(hits) > maxFileNotes {
		hits = hits[:maxFileNotes]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "wong: notes of the brain's project %q cite %s or its folder. Read the ones that bear on what you are about to do:\n", project, rel)
	var paths []string
	for _, h := range hits {
		rel, _ := filepath.Rel(env.Brain, h.path)
		fmt.Fprintf(&b, "- %s (%s)", h.name, link.MemoryDir(env.Brain, env.Home, filepath.ToSlash(rel)))
		if h.desc != "" && h.desc != h.name {
			fmt.Fprintf(&b, ": %s", h.desc)
		}
		b.WriteByte('\n')
		paths = append(paths, h.path)
	}
	remember(env.Brain, in.SessionID, paths)
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "PreToolUse", "additionalContext": b.String()}})
	return string(out)
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

func shownPath(brain, session string) string {
	return filepath.Join(brain, ".wong", "shown", session)
}

// seen returns the notes already shown in a session, so each one comes up once.
func seen(brain, session string) map[string]bool {
	out := map[string]bool{}
	if !sessionRE.MatchString(session) {
		return out
	}
	f, err := os.Open(shownPath(brain, session))
	if err != nil {
		return out
	}
	defer f.Close()
	for s := bufio.NewScanner(f); s.Scan(); {
		out[s.Text()] = true
	}
	return out
}

func remember(brain, session string, paths []string) {
	if !sessionRE.MatchString(session) {
		return
	}
	dir := filepath.Dir(shownPath(brain, session))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 48*time.Hour {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	f, err := os.OpenFile(shownPath(brain, session), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, p := range paths {
		fmt.Fprintln(f, p)
	}
}

// forget drops what a session was shown, when it ends.
func forget(brain, session string) {
	if sessionRE.MatchString(session) {
		os.Remove(shownPath(brain, session))
	}
}
