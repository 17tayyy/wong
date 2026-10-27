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
	Note     string
	Severity Severity
	Ref      string
	Message  string
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
	names   map[string]map[string]bool
	own     map[string][]*repo
	all     []*repo
	commits map[*repo]map[string]string
	teams   map[string]bool
	issues  map[string]linear.Issue
}

func Run(ctx context.Context, opts Options) (Report, error) {
	var rep Report
	c := &checker{names: map[string]map[string]bool{}, own: map[string][]*repo{}, commits: map[*repo]map[string]string{}}
	c.home, _ = os.UserHomeDir()
	notes, err := c.load(opts.Brain, opts.Project)
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
		return cmp.Or(cmp.Compare(a.Note, b.Note), cmp.Compare(a.Severity, b.Severity))
	})
	return rep, nil
}

func (c *checker) load(brain, project string) ([]loaded, error) {
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
		c.names[dir] = map[string]bool{}
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
			c.names[dir][n.Name] = true
			c.names[dir][strings.TrimSuffix(filepath.Base(path), ".md")] = true
			if project != "" && dir != filepath.Join("projects", project) {
				continue
			}
			var verified time.Time
			if !n.Verified.IsZero() {
				verified = n.Verified.AddDate(0, 0, 1)
			}
			notes = append(notes, loaded{
				rel:      filepath.Join(dir, filepath.Base(path)),
				dir:      dir,
				kind:     n.Kind,
				verified: verified,
				asOf:     latest(verified, n.Modified(), info.ModTime()),
				refs:     extract(n.Description + "\n" + n.Body),
			})
		}
	}
	return notes, nil
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
	for _, link := range l.refs.links {
		if !c.names[l.dir][link] && !c.names["me"][link] {
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
		switch {
		case !ok:
			add(Review, id, "not found in Linear")
		case !issue.Closed(), issue.ClosedAt.Before(l.verified):
		case openWord(l.refs.clauses[id]) != "":
			add(Stale, id, fmt.Sprintf("%s since %s, but the note says %q", issue.State, day(issue.ClosedAt), openWord(l.refs.clauses[id])))
		case issue.ClosedAt.After(l.asOf):
			add(Review, id, fmt.Sprintf("%s on %s, after the note was last updated on %s", issue.State, day(issue.ClosedAt), day(l.asOf)))
		}
	}
	return out
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
			return Stale, fmt.Sprintf("deleted or moved in %s on %s, after the note was last updated on %s", d.commit, day(d.when), day(asOf)), true
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

func latest(times ...time.Time) time.Time {
	var t time.Time
	for _, x := range times {
		if x.After(t) {
			t = x
		}
	}
	return t
}

func day(t time.Time) string {
	return t.Format(time.DateOnly)
}
