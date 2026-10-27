// Package check verifies each note in the brain against the files, commits, issues and notes it cites.
package check

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/note"
)

type Severity string

const (
	Stale  Severity = "stale"
	Review Severity = "review"
)

type Finding struct {
	Note     string   `json:"note"`
	Severity Severity `json:"severity"`
	Ref      string   `json:"ref"`
	Message  string   `json:"message"`
}

type Tracker interface {
	TeamKeys(ctx context.Context) ([]string, error)
	Issues(ctx context.Context, ids []string) (map[string]linear.Issue, error)
}

type Options struct {
	Brain   string
	Repos   map[string][]string
	Tracker Tracker
	Project string
	// Note limits the check to one note, given by its path in the brain.
	Note string
}

type Report struct {
	Notes    int
	Findings []Finding
	Skipped  []string
}

type loaded struct {
	rel      string
	dir      string
	kind     note.Kind
	verified time.Time
	asOf     time.Time
	refs     refs
}

type checker struct {
	home    string
	names   map[string]bool
	own     map[string][]*repo
	all     []*repo
	commits map[*repo]map[string]string
	teams   map[string]bool
	issues  map[string]linear.Issue
}

func Run(ctx context.Context, opts Options) (Report, error) {
	var rep Report
	c := &checker{names: map[string]bool{}, own: map[string][]*repo{}, commits: map[*repo]map[string]string{}}
	c.home, _ = os.UserHomeDir()
	notes, err := c.load(opts.Brain, opts.Project, opts.Note)
	if err != nil {
		return rep, err
	}
	rep.Notes = len(notes)

	for _, id := range slices.Sorted(maps.Keys(opts.Repos)) {
		for _, dir := range opts.Repos[id] {
			if _, err := os.Stat(dir); err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("repo %s: %v", dir, err))
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
				continue
			}
			r, err := openRepo(dir)
			if err != nil {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("repo %s: %v", dir, err))
				continue
			}
			c.own["projects/"+id] = append(c.own["projects/"+id], r)
			c.all = append(c.all, r)
		}
	}
	for _, dir := range uniqueDirs(notes) {
		if strings.HasPrefix(dir, "projects/") && len(c.own[dir]) == 0 {
			rep.Skipped = append(rep.Skipped, dir+": no local repos registered, so its files and commits were not checked")
		}
	}

	var shas []string
	for _, l := range notes {
		if len(c.own[l.dir]) > 0 {
			shas = append(shas, l.refs.commits...)
		}
	}
	slices.Sort(shas)
	shas = slices.Compact(shas)
	for _, r := range c.all {
		if c.commits[r], err = r.commits(shas); err != nil {
			return rep, err
		}
	}

	if opts.Tracker == nil {
		rep.Skipped = append(rep.Skipped, "Linear issues: set LINEAR_API_KEY to check them")
	} else if err := c.fetchIssues(ctx, opts.Tracker, notes); err != nil {
		c.teams = nil
		rep.Skipped = append(rep.Skipped, "Linear issues: "+err.Error())
	}

	for _, l := range notes {
		rep.Findings = append(rep.Findings, c.check(l)...)
	}
	stale := map[string]bool{}
	for _, f := range rep.Findings {
		stale[f.Note] = stale[f.Note] || f.Severity == Stale
	}
	slices.SortStableFunc(rep.Findings, func(a, b Finding) int {
		if stale[a.Note] != stale[b.Note] {
			if stale[a.Note] {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.Note, b.Note), cmp.Compare(rank(a.Severity), rank(b.Severity)))
	})
	return rep, nil
}

func (c *checker) load(brain, project, only string) ([]loaded, error) {
	dirs, err := filepath.Glob(filepath.Join(brain, "projects", "*"))
	if err != nil {
		return nil, err
	}
	dirs = append([]string{filepath.Join(brain, "me")}, dirs...)
	if project != "" && !slices.Contains(dirs, filepath.Join(brain, "projects", project)) {
		return nil, fmt.Errorf("no project %q in %s", project, brain)
	}
	var notes []loaded
	for _, abs := range dirs {
		dir, _ := filepath.Rel(brain, abs)
		paths, err := filepath.Glob(filepath.Join(abs, "*.md"))
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
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			rel := filepath.Join(dir, filepath.Base(path))
			c.names[n.Name] = true
			c.names[strings.TrimSuffix(filepath.Base(path), ".md")] = true
			if project != "" && dir != filepath.Join("projects", project) || only != "" && rel != only {
				continue
			}
			notes = append(notes, newLoaded(rel, n, info.ModTime()))
		}
	}
	if only != "" && len(notes) == 0 {
		return nil, fmt.Errorf("no note %s in %s", only, brain)
	}
	return notes, nil
}

func newLoaded(rel string, n *note.Note, written time.Time) loaded {
	var verified time.Time
	if !n.Verified.IsZero() {
		verified = n.Verified.AddDate(0, 0, 1)
	}
	r := extract(n.Description + "\n" + n.Body)
	r.addSources(n.Sources)
	return loaded{
		rel:      rel,
		dir:      filepath.Dir(rel),
		kind:     n.Kind,
		verified: verified,
		asOf:     asOf(verified, n.Modified(), written),
		refs:     r,
	}
}

func (c *checker) fetchIssues(ctx context.Context, t Tracker, notes []loaded) error {
	keys, err := t.TeamKeys(ctx)
	if err != nil {
		return err
	}
	c.teams = map[string]bool{}
	for _, k := range keys {
		c.teams[k] = true
	}
	var ids []string
	for _, l := range notes {
		for _, id := range l.refs.issues {
			if c.isIssue(id) && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	c.issues, err = t.Issues(ctx, ids)
	return err
}

func (c *checker) isIssue(id string) bool {
	team, _, _ := strings.Cut(id, "-")
	return c.teams[team]
}

func (c *checker) check(l loaded) []Finding {
	var out []Finding
	add := func(sev Severity, ref, msg string) {
		out = append(out, Finding{Note: l.rel, Severity: sev, Ref: ref, Message: msg})
	}
	for _, s := range l.refs.unknown {
		add(Review, s, "source not understood: use linear:<ID>, file:<repo>/<path>, commit:<repo>@<sha>, note:<name> or a URL")
	}
	for _, link := range l.refs.links {
		if !c.names[link] {
			add(Stale, "[["+link+"]]", "links to a note that does not exist")
		}
	}
	if own := c.own[l.dir]; len(own) > 0 {
		for _, f := range l.refs.files {
			if sev, msg, found := c.file(f, own, l.asOf); found {
				add(sev, f, msg)
			}
		}
		for _, sha := range l.refs.commits {
			if c.unpushed(sha, own) {
				add(Review, sha, "commit is on no remote branch: squashed, rebased or never pushed")
			}
		}
	}
	for _, id := range l.refs.issues {
		if !c.isIssue(id) || l.kind == note.Preference {
			continue
		}
		issue, ok := c.issues[id]
		if !ok {
			add(Review, id, "not found in Linear")
			continue
		}
		if f, found := l.issueFinding(issue); found {
			out = append(out, f)
		}
	}
	return out
}

// issueFinding applies the rules for a closed issue that the note cites.
func (l loaded) issueFinding(issue linear.Issue) (Finding, bool) {
	id := issue.Identifier
	switch {
	case !issue.Closed(), issue.ClosedAt.Before(l.verified):
	case openWord(l.refs.clauses[id]) != "":
		return Finding{l.rel, Stale, id, fmt.Sprintf("%s since %s, but the note says %q", issue.State, day(issue.ClosedAt), openWord(l.refs.clauses[id]))}, true
	case issue.ClosedAt.After(l.asOf):
		return Finding{l.rel, Review, id, fmt.Sprintf("%s on %s, after the note was last updated on %s", issue.State, day(issue.ClosedAt), day(l.asOf))}, true
	}
	return Finding{}, false
}

func deletedMessage(commit string, when, asOf time.Time) string {
	return fmt.Sprintf("deleted or moved in %s on %s, after the note was last updated on %s", commit, day(when), day(asOf))
}

func (c *checker) file(path string, own []*repo, asOf time.Time) (Severity, string, bool) {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		path = filepath.Join(c.home, rest)
	}
	if filepath.IsAbs(path) && !slices.ContainsFunc(c.all, func(r *repo) bool { _, ok := r.rel(path); return ok }) {
		return "", "", false
	}
	if slices.ContainsFunc(c.all, func(r *repo) bool { return r.has(path) }) {
		return "", "", false
	}
	for _, r := range own {
		if d, ok := r.deletedAt(path); ok {
			if !d.when.After(asOf) {
				return "", "", false
			}
			return Stale, deletedMessage(d.commit, d.when, asOf), true
		}
	}
	if !strings.Contains(path, "/") {
		return "", "", false
	}
	return Review, "not found in the project's repos", true
}

func (c *checker) unpushed(sha string, own []*repo) bool {
	for _, repos := range [][]*repo{own, c.all} {
		for _, r := range repos {
			if full, ok := c.commits[r][sha]; ok {
				return len(r.remote) > 0 && !r.remote[full]
			}
		}
	}
	return false
}

func uniqueDirs(notes []loaded) []string {
	var dirs []string
	for _, l := range notes {
		if !slices.Contains(dirs, l.dir) {
			dirs = append(dirs, l.dir)
		}
	}
	return dirs
}

// asOf is when a note's claims were last confirmed: its verified date if it has one, else its last write.
func asOf(verified, modified, mtime time.Time) time.Time {
	if !verified.IsZero() {
		return verified
	}
	if modified.After(mtime) {
		return modified
	}
	return mtime
}

func rank(s Severity) int {
	if s == Stale {
		return 0
	}
	return 1
}

func day(t time.Time) string {
	return t.Format(time.DateOnly)
}
