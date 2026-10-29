package importer

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	home, projects, brain string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	tmp := t.TempDir()
	f := fixture{
		home:     filepath.Join(tmp, "home", "dev"),
		projects: filepath.Join(tmp, "claude", "projects"),
		brain:    filepath.Join(tmp, "brain"),
	}
	for _, repo := range []string{"Acme/acme-api", "Acme/acme-web", "side_project"} {
		dir := filepath.Join(f.home, repo)
		mkdir(t, dir)
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}
	mkdir(t, filepath.Join(f.home, "Acme", "acme-api", "app"))
	return f
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func noteText(name, claudeType string) string {
	return "---\nname: " + name + "\ndescription: about " + name + "\nmetadata:\n  type: " + claudeType + "\n---\nBody of " + name + ".\n"
}

// memory creates the Claude Code memory folder for dir with the given notes and index.
func (f fixture) memory(t *testing.T, dir string, notes map[string]string, index string) string {
	t.Helper()
	folder := claudeUnsafeRE.ReplaceAllString(dir, "-")
	mem := filepath.Join(f.projects, folder, "memory")
	mkdir(t, mem)
	for file, content := range notes {
		write(t, filepath.Join(mem, file), content)
	}
	if index != "" {
		write(t, filepath.Join(mem, "MEMORY.md"), index)
	}
	return folder
}

func (f fixture) acme(t *testing.T) {
	t.Helper()
	f.memory(t, filepath.Join(f.home, "Acme", "acme-api"), map[string]string{
		"use-uv.md":    noteText("use-uv", "feedback"),
		"multi-org.md": noteText("multi-org", "project"),
		"shopify.md":   noteText("shopify", "project"),
	}, "# Memory Index\n\n## Forma de trabajar\n- [Use uv](use-uv.md) — uv run\n\n## Proyecto\n- [Multi-org](multi-org.md) · [Shopify](shopify.md) — memberships\n")
	f.memory(t, filepath.Join(f.home, "Acme", "acme-web"), map[string]string{
		"landing.md": noteText("landing", "project"),
	}, "# Memory Index\n\n- [Landing](landing.md) — owned by marketing\n")
	f.memory(t, filepath.Join(f.home, "Acme"), map[string]string{
		"stack.md":   noteText("stack", "project"),
		"db-envs.md": noteText("db-envs", "reference"),
	}, "# Memory Index\n\n- [Stack](stack.md) — three repos\n")
}

func plan(t *testing.T, f fixture, overrides map[string]string) []Source {
	t.Helper()
	sources, err := Plan(f.projects, f.home, overrides)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return sources
}

func targets(sources []Source) map[string]string {
	out := map[string]string{}
	for _, s := range sources {
		out[s.Label()] = s.Target
	}
	return out
}

func TestPlanGroupsReposByParentDirectory(t *testing.T) {
	f := newFixture(t)
	f.acme(t)
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"a.md": noteText("a", "project")}, "")
	f.memory(t, filepath.Join(f.home, "Acme", "acme-api", "app"), map[string]string{"b.md": noteText("b", "project")}, "")
	f.memory(t, f.home, map[string]string{"me.md": noteText("me", "user")}, "")
	f.memory(t, filepath.Join(f.home, "empty"), map[string]string{}, "# Memory Index\n")
	mkdir(t, filepath.Join(f.home, "empty"))

	got := targets(plan(t, f, nil))

	want := map[string]string{
		"acme-api":     "projects/acme",
		"acme-web":     "projects/acme",
		"Acme":         "projects/acme",
		"app":          "projects/acme",
		"side_project": "projects/side_project",
		"dev":          "me",
	}
	if len(got) != len(want) {
		t.Errorf("targets = %v", got)
	}
	for label, target := range want {
		if got[label] != target {
			t.Errorf("%s -> %q, want %q", label, got[label], target)
		}
	}
}

func TestPlanRecordsTheRepoOfEachFolder(t *testing.T) {
	f := newFixture(t)
	f.acme(t)
	f.memory(t, filepath.Join(f.home, "Acme", "acme-api", "app"), map[string]string{"b.md": noteText("b", "project")}, "")

	backend := filepath.Join(f.home, "Acme", "acme-api")
	want := map[string]string{"acme-api": backend, "app": backend, "acme-web": filepath.Join(f.home, "Acme", "acme-web"), "Acme": ""}
	for _, s := range plan(t, f, nil) {
		if s.Repo != want[s.Label()] {
			t.Errorf("%s: Repo = %q, want %q", s.Label(), s.Repo, want[s.Label()])
		}
	}
}

func TestPlanOverridesByPathOrFolder(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.home, "side_project")
	f.memory(t, dir, map[string]string{"a.md": noteText("a", "project")}, "")
	gone := f.memory(t, filepath.Join(f.home, "deleted-repo"), map[string]string{"b.md": noteText("b", "project")}, "")

	got := targets(plan(t, f, map[string]string{dir: "sideproject", gone: "me"}))

	if got["side_project"] != "projects/sideproject" || got[gone] != "me" {
		t.Errorf("targets = %v", got)
	}
}

func TestPlanFailsOnFolderWithoutDirectory(t *testing.T) {
	f := newFixture(t)
	gone := f.memory(t, filepath.Join(f.home, "deleted-repo"), map[string]string{"b.md": noteText("b", "project")}, "")

	_, err := Plan(f.projects, f.home, nil)
	if err == nil || !strings.Contains(err.Error(), gone) || !strings.Contains(err.Error(), "--map") {
		t.Errorf("Plan() error = %v", err)
	}
}

func TestPlanRejectsInvalidProjectID(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.home, "side_project")
	f.memory(t, dir, map[string]string{"a.md": noteText("a", "project")}, "")

	if _, err := Plan(f.projects, f.home, map[string]string{dir: "Zona 14"}); err == nil {
		t.Error("Plan() accepted an invalid project id")
	}
}

func TestApplyWritesNotesWithKindAndScope(t *testing.T) {
	f := newFixture(t)
	f.acme(t)
	f.memory(t, f.home, map[string]string{"me.md": noteText("me", "user")}, "")

	res, err := Apply(f.brain, plan(t, f, nil), false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if res.Written != 7 || res.Unchanged != 0 {
		t.Errorf("result = %+v", res)
	}
	tests := map[string][]string{
		"projects/acme/use-uv.md":  {"kind: preference\n", "scope: project:acme\n", "  type: feedback\n", "Body of use-uv.\n"},
		"projects/acme/db-envs.md": {"kind: reference\n", "scope: project:acme\n"},
		"me/me.md":                 {"kind: preference\n", "scope: global\n"},
	}
	for rel, wants := range tests {
		got := read(t, filepath.Join(f.brain, rel))
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %q:\n%s", rel, want, got)
			}
		}
	}
}

func TestApplyMergesIndexesOfOneProject(t *testing.T) {
	f := newFixture(t)
	f.acme(t)

	if _, err := Apply(f.brain, plan(t, f, nil), false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := `# Memory Index

## acme-api

### Forma de trabajar
- [Use uv](use-uv.md) — uv run

### Proyecto
- [Multi-org](multi-org.md) · [Shopify](shopify.md) — memberships

## Acme

- [Stack](stack.md) — three repos

## acme-web

- [Landing](landing.md) — owned by marketing

- [db-envs](db-envs.md) — about db-envs
`
	if got := read(t, filepath.Join(f.brain, "projects/acme/MEMORY.md")); got != want {
		t.Errorf("MEMORY.md =\n%s\nwant\n%s", got, want)
	}
}

func TestApplyKeepsSingleIndexVerbatim(t *testing.T) {
	f := newFixture(t)
	index := "# Memory Index\n\n## Notes\n- [A](a.md) — curated line\n"
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"a.md": noteText("a", "project")}, index)

	if _, err := Apply(f.brain, plan(t, f, nil), false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := read(t, filepath.Join(f.brain, "projects/side_project/MEMORY.md")); got != index {
		t.Errorf("MEMORY.md = %q", got)
	}
}

func TestApplyWarnsAboutLongIndexes(t *testing.T) {
	f := newFixture(t)
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"a.md": noteText("a", "project")},
		"# Memory Index\n"+strings.Repeat("- line\n", indexMaxLines))

	res, err := Apply(f.brain, plan(t, f, nil), true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "only loads the first 200") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.acme(t)
	sources := plan(t, f, nil)
	if _, err := Apply(f.brain, sources, false); err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	res, err := Apply(f.brain, sources, false)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if res.Written != 0 || res.Unchanged != 6 {
		t.Errorf("result = %+v", res)
	}
}

func TestApplyKeepsModificationTimes(t *testing.T) {
	f := newFixture(t)
	f.acme(t)
	sources := plan(t, f, nil)
	src := sources[0].Notes[0]
	written := time.Date(2026, 7, 29, 22, 3, 47, 0, time.UTC)
	if err := os.Chtimes(src, written, written); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(f.brain, sources, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Stat(filepath.Join(f.brain, sources[0].Target, filepath.Base(src)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(written) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), written)
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.acme(t)

	res, err := Apply(f.brain, plan(t, f, nil), true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Written != 6 {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(f.brain); !os.IsNotExist(err) {
		t.Errorf("brain exists after dry run: %v", err)
	}
}

func TestApplyDeduplicatesIdenticalNotes(t *testing.T) {
	f := newFixture(t)
	same := noteText("use-uv", "feedback")
	f.memory(t, filepath.Join(f.home, "Acme", "acme-api"), map[string]string{"use-uv.md": same}, "")
	f.memory(t, filepath.Join(f.home, "Acme", "acme-web"), map[string]string{"use-uv.md": same}, "")

	res, err := Apply(f.brain, plan(t, f, nil), false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Written != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestApplyRefusesTwoFoldersWithDifferentNotesOfTheSameName(t *testing.T) {
	f := newFixture(t)
	f.memory(t, filepath.Join(f.home, "Acme", "acme-api"), map[string]string{"use-uv.md": noteText("use-uv", "feedback")}, "")
	f.memory(t, filepath.Join(f.home, "Acme", "acme-web"), map[string]string{"use-uv.md": noteText("use-uv", "project")}, "")
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"a.md": noteText("a", "project")}, "")

	_, err := Apply(f.brain, plan(t, f, nil), false)

	if err == nil || !strings.Contains(err.Error(), "projects/acme/use-uv.md: two memory folders") {
		t.Fatalf("Apply() error = %v", err)
	}
	if _, err := os.Stat(f.brain); !os.IsNotExist(err) {
		t.Errorf("the brain was written despite the conflict")
	}
}

// reimport imports the fixture, lets change alter brain and sources, and imports again.
func reimport(t *testing.T, change func(f fixture, src, dst string)) (Result, fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"a.md": noteText("a", "project")}, "")
	sources := plan(t, f, nil)
	if _, err := Apply(f.brain, sources, false); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	dst := filepath.Join(f.brain, "projects/side_project/a.md")
	change(f, sources[0].Notes[0], dst)
	res, err := Apply(f.brain, plan(t, f, nil), false)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	return res, f, dst
}

func TestApplyUpdatesNotesTheBrainHasNotChanged(t *testing.T) {
	res, _, dst := reimport(t, func(_ fixture, src, _ string) {
		write(t, src, noteText("a", "project")+"Added later in Claude Code.\n")
	})

	if res.Updated != 1 || res.Written != 0 || len(res.Conflicts) != 0 {
		t.Errorf("result = %+v", res)
	}
	if got := read(t, dst); !strings.Contains(got, "Added later in Claude Code.") {
		t.Errorf("brain note = %q", got)
	}
}

func TestApplyKeepsNotesChangedInTheBrain(t *testing.T) {
	res, _, dst := reimport(t, func(_ fixture, _, dst string) {
		write(t, dst, read(t, dst)+"Fixed in the brain.\n")
	})

	if res.Kept != 1 || res.Updated != 0 || len(res.Conflicts) != 0 {
		t.Errorf("result = %+v", res)
	}
	if got := read(t, dst); !strings.Contains(got, "Fixed in the brain.") {
		t.Errorf("brain note = %q", got)
	}
}

func TestApplyReportsNotesChangedOnBothSides(t *testing.T) {
	res, _, dst := reimport(t, func(_ fixture, src, dst string) {
		write(t, src, noteText("a", "project")+"Added later in Claude Code.\n")
		write(t, dst, read(t, dst)+"Fixed in the brain.\n")
	})

	if !slices.Equal(res.Conflicts, []string{"projects/side_project/a.md"}) || res.Updated != 0 {
		t.Errorf("result = %+v", res)
	}
	if got := read(t, dst); !strings.Contains(got, "Fixed in the brain.") || strings.Contains(got, "Added later") {
		t.Errorf("brain note = %q", got)
	}
}

func TestApplyReportsBrainNotesItNeverImported(t *testing.T) {
	f := newFixture(t)
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"a.md": noteText("a", "project"), "b.md": noteText("b", "project")}, "")
	write(t, filepath.Join(f.brain, "projects/side_project/a.md"), "written by hand\n")

	res, err := Apply(f.brain, plan(t, f, nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Conflicts, []string{"projects/side_project/a.md"}) || res.Written != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestApplyFailsOnUnreadableNote(t *testing.T) {
	f := newFixture(t)
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"bad.md": "no frontmatter"}, "")

	_, err := Apply(f.brain, plan(t, f, nil), false)
	if err == nil || !strings.Contains(err.Error(), "bad.md: missing frontmatter") {
		t.Errorf("Apply() error = %v", err)
	}
}

func TestPlanKeepsReposInSharedFoldersApart(t *testing.T) {
	f := newFixture(t)
	for _, repo := range []string{"code/api", "code/web"} {
		dir := filepath.Join(f.home, repo)
		mkdir(t, dir)
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		f.memory(t, dir, map[string]string{"a.md": noteText("a", "project")}, "")
	}
	got := targets(plan(t, f, nil))
	if got["api"] != "projects/api" || got["web"] != "projects/web" {
		t.Errorf("targets = %v", got)
	}
}

func TestApplyAddsToAnIndexTheBrainAlreadyHas(t *testing.T) {
	f := newFixture(t)
	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"a.md": noteText("a", "project")}, "")
	index := filepath.Join(f.brain, "projects/side_project/MEMORY.md")
	write(t, index, "- [From the server](server.md) — pulled from another machine\n")

	res, err := Apply(f.brain, plan(t, f, nil), false)
	if err != nil {
		t.Fatal(err)
	}
	want := "- [From the server](server.md) — pulled from another machine\n- [a](a.md) — about a\n"
	if got := read(t, index); got != want || len(res.Conflicts) != 0 || res.Written != 1 {
		t.Errorf("index = %q, result = %+v", got, res)
	}

	f.memory(t, filepath.Join(f.home, "side_project"), map[string]string{"b.md": noteText("b", "project")}, "")
	if res, err = Apply(f.brain, plan(t, f, nil), false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, index); got != want+"- [b](b.md) — about b\n" || len(res.Conflicts) != 0 {
		t.Errorf("index after a second import = %q, result = %+v", got, res)
	}
}
