package hook

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	brainfs "github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/link"
	"github.com/17tayyy/wong/internal/note"
)

const (
	maxFileNotes    = 5
	maxNoteFindings = 2
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
	sessionRE      = regexp.MustCompile(`^[\w-]{1,128}$`)
)

// FileNotes answers PreToolUse on files with the notes of their project that cite them or their folder, each once per session.
func FileNotes(in Input, env Env) string {
	files, _ := touched(in, env, true)
	var blocks []string
	for _, file := range files {
		if block := fileNotes(file, in.SessionID, env); block != "" {
			blocks = append(blocks, block)
		}
	}
	return answer(env.Agent, "PreToolUse", strings.Join(blocks, "\n"))
}

// Hit is a note of the brain that cites a file or its folder.
type Hit struct {
	Path, Name, Description string
	Pitfall                 bool
	score                   int
}

// Citing returns the project of a file in a linked repo, the file's path in its repo, and the notes of that project that
// cite it or its folder or apply to it: pitfalls first, then the closest.
func Citing(file string, env Env) (project, rel string, hits []Hit) {
	file = filepath.Clean(file)
	if !filepath.IsAbs(file) || strings.HasPrefix(file, filepath.Clean(env.Brain)+string(filepath.Separator)) {
		return "", "", nil
	}
	project, dir := env.Repos.Containing(file)
	if project == "" {
		return "", "", nil
	}
	rel, err := filepath.Rel(dir, file)
	if err != nil {
		return "", "", nil
	}
	rel, repo := filepath.ToSlash(rel), filepath.Base(dir)
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
		n, err := note.Parse(data)
		if err == nil && best == 0 && slices.ContainsFunc(n.AppliesTo, func(g string) bool {
			return note.MatchGlob(g, rel) || note.MatchGlob(g, repo+"/"+rel)
		}) {
			best = 1
		}
		if best == 0 {
			continue
		}
		h := Hit{Path: p, Name: strings.TrimSuffix(filepath.Base(p), ".md"), score: best}
		if err == nil {
			h.Name, h.Description, h.Pitfall = n.Name, n.Description, n.Kind == note.Pitfall
		}
		hits = append(hits, h)
	}
	slices.SortFunc(hits, func(a, b Hit) int {
		if a.Pitfall != b.Pitfall {
			if a.Pitfall {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.Name, b.Name))
	})
	return project, rel, hits
}

// fileNotes lists the notes that cite a file or its folder and that the session has not seen yet, and remembers them as seen.
func fileNotes(file, session string, env Env) string {
	project, rel, hits := Citing(file, env)
	shown := seen(env.Brain, session)
	hits = slices.DeleteFunc(hits, func(h Hit) bool { return shown[h.Path] })
	if len(hits) == 0 {
		return ""
	}
	if len(hits) > maxFileNotes {
		hits = hits[:maxFileNotes]
	}
	findings := map[string][]check.Finding{}
	if rep, err := check.Run(context.Background(), check.Options{Brain: env.Brain, Repos: env.Repos, Project: project}); err == nil {
		for _, f := range rep.Findings {
			findings[f.Note] = append(findings[f.Note], f)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "wong: notes of the brain's project %q cite %s or its folder, or apply to it. Read the ones that bear on what you are about to do:\n", project, rel)
	var paths []string
	doubts := false
	for _, h := range hits {
		rel, _ := filepath.Rel(env.Brain, h.Path)
		rel = filepath.ToSlash(rel)
		b.WriteString(Line(h, env))
		for i, f := range findings[rel] {
			if i == maxNoteFindings {
				fmt.Fprintf(&b, "  - and %d more (`wong note show %s`)\n", len(findings[rel])-maxNoteFindings, h.Name)
				break
			}
			says := "it may be outdated"
			if f.Severity == check.Stale {
				says = "it contradicts its sources"
			}
			fmt.Fprintf(&b, "  - %s: %s %s\n", says, f.Ref, f.Message)
			doubts = true
		}
		paths = append(paths, h.Path)
	}
	if doubts {
		fmt.Fprintf(&b, "Check a note marked so against its sources before relying on it, and fix it with the %s skill once you have finished what the user asked.\n", skill(env.Agent))
	}
	remember(env.Brain, session, paths)
	return b.String()
}

// Line is how a note on a file is listed: its name, where it is, whether it is a pitfall, and its description.
func Line(h Hit, env Env) string {
	rel, _ := filepath.Rel(env.Brain, h.Path)
	line := fmt.Sprintf("- %s (%s)", h.Name, link.MemoryDir(env.Brain, env.Home, filepath.ToSlash(rel)))
	if h.Pitfall {
		line += ", a pitfall"
	}
	if h.Description != "" && h.Description != h.Name {
		line += ": " + h.Description
	}
	return line + "\n"
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
	if err := os.MkdirAll(dir, 0o755); err != nil || brainfs.IgnoreState(brain, "shown/") != nil {
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
