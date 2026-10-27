package check

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/linear"
)

type fakeTracker struct {
	issues map[string]linear.Issue
	err    error
}

func (f fakeTracker) TeamKeys(context.Context) ([]string, error) {
	return []string{"ENG"}, f.err
}

func (f fakeTracker) Issues(_ context.Context, ids []string) (map[string]linear.Issue, error) {
	out := map[string]linear.Issue{}
	for _, id := range ids {
		if i, ok := f.issues[id]; ok {
			out[id] = i
		}
	}
	return out, nil
}

var tracker = fakeTracker{issues: map[string]linear.Issue{
	"ENG-1": {Identifier: "ENG-1", State: "Done", Type: "completed", ClosedAt: date("2026-10-02")},
	"ENG-2": {Identifier: "ENG-2", State: "Canceled", Type: "canceled", ClosedAt: date("2026-10-20")},
	"ENG-3": {Identifier: "ENG-3", State: "In Progress", Type: "started"},
}}

func date(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	brain, repo                string
	initial, deleted, unpushed string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
	} {
		t.Setenv(k, v)
	}
	tmp := t.TempDir()
	f := fixture{brain: filepath.Join(tmp, "brain"), repo: filepath.Join(tmp, "Acme", "acme-api")}
	origin := filepath.Join(tmp, "origin.git")
	run(t, tmp, "init", "-q", "--bare", origin)
	run(t, tmp, "init", "-q", "-b", "main", f.repo)
	write(t, filepath.Join(f.repo, "app/models.py"), "class Membership: ...\n")
	write(t, filepath.Join(f.repo, "app/old.py"), "old\n")
	write(t, filepath.Join(f.repo, "docs/guide.md"), "# Guide\n")
	run(t, f.repo, "add", ".")
	commitAt(t, f.repo, "2026-08-01T12:00:00Z", "init")
	f.initial = run(t, f.repo, "rev-parse", "--short", "HEAD")
	run(t, f.repo, "rm", "-q", "app/old.py")
	commitAt(t, f.repo, "2026-09-15T12:00:00Z", "drop old")
	f.deleted = run(t, f.repo, "rev-parse", "--short", "HEAD")
	run(t, f.repo, "remote", "add", "origin", origin)
	run(t, f.repo, "push", "-q", "origin", "main")
	write(t, filepath.Join(f.repo, "app/local.py"), "local\n")
	run(t, f.repo, "add", ".")
	commitAt(t, f.repo, "2026-10-01T12:00:00Z", "local only")
	f.unpushed = run(t, f.repo, "rev-parse", "--short", "HEAD")
	return f
}

func commitAt(t *testing.T, dir, when, msg string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_DATE", when)
	t.Setenv("GIT_COMMITTER_DATE", when)
	run(t, dir, "commit", "-q", "-m", msg)
}

func (f fixture) note(t *testing.T, rel, body, modified string) {
	t.Helper()
	path := filepath.Join(f.brain, rel)
	name := strings.TrimSuffix(filepath.Base(rel), ".md")
	write(t, path, "---\nname: "+name+"\ndescription: d\nmetadata:\n  type: project\n---\n"+body)
	if modified != "" {
		if err := os.Chtimes(path, date(modified), date(modified)); err != nil {
			t.Fatal(err)
		}
	}
}

func (f fixture) options(tr Tracker) Options {
	return Options{Brain: f.brain, Repos: map[string][]string{"acme": {f.repo}}, Tracker: tr}
}

func found(rep Report) []string {
	var out []string
	for _, f := range rep.Findings {
		out = append(out, string(f.Severity)+" "+f.Note+" "+f.Ref)
	}
	return out
}

func TestRun(t *testing.T) {
	f := newFixture(t)
	f.note(t, "projects/acme/ok.md", "Uses `app/models.py:12`, `models.py`, `acme-api/docs/guide.md` and `app/local.py`.\n"+
		"Shipped in "+f.initial+". See [[review]] and [[global]]. ENG-1 is done. ENG-3 is In Progress.\n"+
		"Removed `app/old.py` before this note was written.\n"+
		"Ids like 5f3a9c1e7b2d4f6a8c0e1b3d, 12ab34cd-56ef-4929 and ABC-1 are not references.\n", "2026-10-25")
	f.note(t, "projects/acme/stale.md", "Logic lives in `app/old.py`. See [[gone]].\nENG-1 en pausa until the app is approved.\n", "2026-09-01")
	f.note(t, "projects/acme/review.md", "Plan: `app/never.py`, scratch `smoke.py`. Local work in "+f.unpushed+", unknown deadbee1.\n"+
		"ENG-2 is the next step. ENG-404 too.\n", "2026-10-10")
	f.note(t, "me/global.md", "Applies everywhere, even `app/never.py`. ENG-1 pendiente.\n", "2026-10-25")

	rep, err := Run(context.Background(), f.options(tracker))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"stale me/global.md ENG-1",
		"stale projects/acme/stale.md [[gone]]",
		"stale projects/acme/stale.md app/old.py",
		"stale projects/acme/stale.md ENG-1",
		"review projects/acme/review.md app/never.py",
		"review projects/acme/review.md " + f.unpushed,
		"review projects/acme/review.md ENG-2",
		"review projects/acme/review.md ENG-404",
	}
	if got := found(rep); !slices.Equal(got, want) {
		t.Errorf("findings =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rep.Notes != 4 || len(rep.Skipped) != 0 {
		t.Errorf("notes = %d, skipped = %v", rep.Notes, rep.Skipped)
	}
	messages := map[string]string{}
	for _, finding := range rep.Findings {
		messages[finding.Ref] = finding.Message
	}
	if want := "deleted or moved in " + f.deleted + " on 2026-09-15, after the note was last updated on 2026-09-01"; messages["app/old.py"] != want {
		t.Errorf("app/old.py message = %q, want %q", messages["app/old.py"], want)
	}
	if want := "Canceled on 2026-10-20, after the note was last updated on 2026-10-10"; messages["ENG-2"] != want {
		t.Errorf("ENG-2 message = %q, want %q", messages["ENG-2"], want)
	}
}

func TestRunWithoutTrackerOrRepos(t *testing.T) {
	f := newFixture(t)
	f.note(t, "projects/acme/a.md", "Gone: `app/old.py`. ENG-1 en pausa.\n", "2026-09-01")
	f.note(t, "projects/other/b.md", "Never: `app/never.py`.\n", "")

	rep, err := Run(context.Background(), Options{Brain: f.brain, Repos: map[string][]string{"acme": {f.repo}}})
	if err != nil {
		t.Fatal(err)
	}

	if got := found(rep); !slices.Equal(got, []string{"stale projects/acme/a.md app/old.py"}) {
		t.Errorf("findings = %v", got)
	}
	skipped := strings.Join(rep.Skipped, "\n")
	if !strings.Contains(skipped, "projects/other: no local repos registered") || !strings.Contains(skipped, "LINEAR_API_KEY") {
		t.Errorf("skipped = %v", rep.Skipped)
	}
}

func TestRunDoesNotCheckIssueStatesInPreferences(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.brain, "projects/acme/move-issues.md"),
		"---\nname: move-issues\ndescription: d\nkind: preference\n---\nOn 2026-09-01 I started ENG-1 and left it en pausa.\n")

	rep, err := Run(context.Background(), f.options(tracker))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings = %v", found(rep))
	}
}

func TestRunReadsTheDescription(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.brain, "projects/acme/remove.md"),
		"---\nname: remove\ndescription: ENG-1 en pausa until Meta answers\n---\nBody.\n")

	rep, err := Run(context.Background(), f.options(tracker))
	if err != nil {
		t.Fatal(err)
	}
	if got := found(rep); !slices.Equal(got, []string{"stale projects/acme/remove.md ENG-1"}) {
		t.Errorf("findings = %v", got)
	}
}

func TestRunChecksDeclaredSources(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.brain, "projects/acme/declared.md"), "---\nname: declared\ndescription: d\nsources:\n"+
		"  - linear:ENG-2\n  - linear:ENG-3\n  - file:acme-api/app/models.py\n  - file:acme-api/app/never.py\n"+
		"  - commit:acme-api@"+f.unpushed+"\n  - note:gone\n  - https://example.com/spec\n  - jira:ENG-2\n"+
		"verified: 2026-10-10\n---\nNo references in the text.\n")

	rep, err := Run(context.Background(), f.options(tracker))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"stale projects/acme/declared.md [[gone]]",
		"review projects/acme/declared.md jira:ENG-2",
		"review projects/acme/declared.md acme-api/app/never.py",
		"review projects/acme/declared.md " + f.unpushed,
		"review projects/acme/declared.md ENG-2",
	}
	if got := found(rep); !slices.Equal(got, want) {
		t.Errorf("findings =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRunTrustsVerificationAfterTheIssueClosed(t *testing.T) {
	f := newFixture(t)
	for name, verified := range map[string]string{"same-day": "2026-10-02", "later": "2026-10-05", "before": "2026-10-01"} {
		write(t, filepath.Join(f.brain, "projects/acme/"+name+".md"),
			"---\nname: "+name+"\ndescription: d\nverified: "+verified+"\n---\nENG-1 was en pausa until the app review.\n")
	}

	rep, err := Run(context.Background(), f.options(tracker))
	if err != nil {
		t.Fatal(err)
	}
	if got := found(rep); !slices.Equal(got, []string{"stale projects/acme/before.md ENG-1"}) {
		t.Errorf("findings = %v", got)
	}
}

func TestRunSkipsLinkedFoldersThatAreNotRepos(t *testing.T) {
	f := newFixture(t)
	f.note(t, "projects/acme/a.md", "Fine.\n", "")
	folder := filepath.Dir(f.repo)

	opts := f.options(nil)
	opts.Repos["acme"] = append(opts.Repos["acme"], folder, filepath.Join(folder, "deleted"))
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	skipped := strings.Join(rep.Skipped, "\n")
	if strings.Contains(skipped, "repo "+folder+":") || !strings.Contains(skipped, "repo "+filepath.Join(folder, "deleted")+":") {
		t.Errorf("skipped = %v", rep.Skipped)
	}
}

func TestRunReportsTrackerErrorsAsSkipped(t *testing.T) {
	f := newFixture(t)
	f.note(t, "projects/acme/a.md", "ENG-1 en pausa.\n", "")

	rep, err := Run(context.Background(), f.options(fakeTracker{err: errors.New("Authentication required")}))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 || !slices.Contains(rep.Skipped, "Linear issues: Authentication required") {
		t.Errorf("findings = %v, skipped = %v", found(rep), rep.Skipped)
	}
}

func TestRunOneProject(t *testing.T) {
	f := newFixture(t)
	f.note(t, "projects/acme/a.md", "See [[b]] and [[gone]].\n", "")
	f.note(t, "projects/other/b.md", "See [[nothing]].\n", "")

	rep, err := Run(context.Background(), Options{Brain: f.brain, Project: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Notes != 1 || len(rep.Findings) != 1 || rep.Findings[0].Ref != "[[gone]]" {
		t.Errorf("notes = %d, findings = %v", rep.Notes, found(rep))
	}

	if _, err := Run(context.Background(), Options{Brain: f.brain, Project: "missing"}); err == nil {
		t.Error("Run() accepted an unknown project")
	}
}

func TestRunOneNote(t *testing.T) {
	f := newFixture(t)
	f.note(t, "projects/acme/a.md", "See [[gone]].\n", "")
	f.note(t, "projects/acme/b.md", "See [[nothing]].\n", "")

	rep, err := Run(context.Background(), Options{Brain: f.brain, Note: "projects/acme/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if got := found(rep); rep.Notes != 1 || !slices.Equal(got, []string{"stale projects/acme/a.md [[gone]]"}) {
		t.Errorf("notes = %d, findings = %v", rep.Notes, got)
	}
	if _, err := Run(context.Background(), Options{Brain: f.brain, Note: "projects/acme/zzz.md"}); err == nil {
		t.Error("Run() accepted an unknown note")
	}
}

func TestLinksResolveAcrossTheBrain(t *testing.T) {
	f := newFixture(t)
	f.note(t, "projects/acme/a.md", "See [[b]].\n", "")
	f.note(t, "projects/other/b.md", "Fine.\n", "")

	rep, err := Run(context.Background(), Options{Brain: f.brain})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings = %v", found(rep))
	}
}

func TestValidSource(t *testing.T) {
	for s, want := range map[string]bool{
		"linear:ENG-15": true, "file:acme-api/app/x.py": true, "commit:acme-api@1a2b3c4": true,
		"note:other": true, "https://example.com": true,
		"linear:eng-15": false, "file:x.py": false, "commit:1a2b3c4": false, "jira:ENG-1": false, "note:": false,
	} {
		if got := ValidSource(s); got != want {
			t.Errorf("ValidSource(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestFileRef(t *testing.T) {
	tests := map[string]string{
		"app/models.py":                       "app/models.py",
		"./app/models.py:42":                  "app/models.py",
		"app/x.py#Membership":                 "app/x.py",
		"pyproject.toml":                      "pyproject.toml",
		"acme/backend/prod":                   "",
		"x-forwarded-for/proto":               "",
		"uv run pytest":                       "",
		"app/.../service.ts":                  "",
		"https://x.dev/a.md":                  "",
		"orca.exe":                            "",
		".css":                                "",
		"app.example.com/assets/i18n/es.json": "",
		"src/*.py":                            "",
		"C:\\Users\\dev\\a.json":              "",
	}
	for token, want := range tests {
		if got, _ := fileRef(token); got != want {
			t.Errorf("fileRef(%q) = %q, want %q", token, got, want)
		}
	}
}

func TestExtractCommits(t *testing.T) {
	body := "Fixed in 1a2b3c4 and 9f8e7d6, see abc1234def5678abc1234def5678abc1234def56.\n" +
		"Not commits: 1234567, deadbeef, ObjectId 5f3a9c1e7b2d4f6a8c0e1b3d, uuid 12ab34cd-56ef, v1.2.3abc4567, cafe1234567890a, abc1234.js."
	want := []string{"1a2b3c4", "9f8e7d6", "abc1234def5678abc1234def5678abc1234def56"}
	if got := extract(body).commits; !slices.Equal(got, want) {
		t.Errorf("commits = %v, want %v", got, want)
	}
}

func TestOpenWordOnlyLooksAtTheIssueClause(t *testing.T) {
	r := extract("ENG-1 shipped; ENG-2 en pausa. ENG-1 — done\nENG-3 is In Progress\n" +
		"Created (Backlog): ENG-4 quota (Urgent, Todo), ENG-5 tickets, ENG-6 bloqueada por Meta, ENG-7 cerrado, ENG-8 sin empezar")
	tests := map[string]string{"ENG-4": "Backlog", "ENG-5": "", "ENG-6": "bloqueada", "ENG-7": "", "ENG-8": "sin empezar"}
	for id, want := range tests {
		if got := openWord(r.clauses[id]); got != want {
			t.Errorf("%s open word = %q, want %q", id, got, want)
		}
	}

	if w := openWord(r.clauses["ENG-1"]); w != "" {
		t.Errorf("ENG-1 open word = %q", w)
	}
	if w := openWord(r.clauses["ENG-2"]); w != "en pausa" {
		t.Errorf("ENG-2 open word = %q", w)
	}
	if w := openWord(r.clauses["ENG-3"]); w != "In Progress" {
		t.Errorf("ENG-3 open word = %q", w)
	}
}
