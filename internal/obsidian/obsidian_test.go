package obsidian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/note"
)

func put(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
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

func brainDir(t *testing.T) string {
	dir := t.TempDir()
	put(t, dir, "me/use-uv.md", "---\nname: use-uv\ndescription: uv, never pip\nkind: preference\nscope: global\n---\nUse uv.\n")
	put(t, dir, "me/MEMORY.md", "- [use-uv](use-uv.md) — uv, never pip\n")
	put(t, dir, "projects/acme/answer-in-spanish.md", "---\nname: Answer in Spanish\ndescription: Spanish replies\nkind: preference\nscope: project:acme\n---\nAlways.\n")
	put(t, dir, "projects/acme/existing.md", "---\nname: existing\ndescription: already here\nkind: reference\nscope: project:acme\n---\nHere.\n")
	put(t, dir, "projects/acme/deploy.md", "---\nname: deploy\ndescription: How Acme ships\nkind: reference\nscope: project:acme\n---\nSee [[Answer in Spanish]], [[Answer in Spanish#Why|why]] and [[use-uv]].\n")
	put(t, dir, "projects/acme/MEMORY.md", "- [deploy](deploy.md) — How Acme ships\n")
	return dir
}

func TestExport(t *testing.T) {
	brain := brainDir(t)
	vault := filepath.Join(t.TempDir(), "vault")
	rep, err := Export(brain, vault)
	if err != nil || rep.Notes != 4 || rep.Indexes != 2 || rep.Links != 2 {
		t.Fatalf("Export = %+v, %v", rep, err)
	}
	got := read(t, filepath.Join(vault, "projects/acme/deploy.md"))
	if !strings.Contains(got, "See [[answer-in-spanish|Answer in Spanish]], [[answer-in-spanish#Why|why]] and [[use-uv]].") {
		t.Errorf("exported links:\n%s", got)
	}
	if read(t, filepath.Join(vault, "me/MEMORY.md")) != "- [use-uv](use-uv.md) — uv, never pip\n" {
		t.Error("the index was not copied as it is")
	}
	if _, err := Export(brain, vault); err == nil {
		t.Error("an export over a folder with files in it")
	}
}

func TestImport(t *testing.T) {
	brain := brainDir(t)
	vault := t.TempDir()
	put(t, vault, "Daily Plan.md", "# Plan for the launch\nShip on Monday #launch and #Project/Alpha, not `#code` or #123.\n![[shot.png]] See [[Other Note]] and [[Ghost]].\n")
	put(t, vault, "research/Other Note.md", "---\ntags: [Research]\ndescription: Notes from the user interviews\nstatus: done\n---\nInterviews went well.\n")
	put(t, vault, "research/existing.md", "Would overwrite the brain.\n")
	put(t, vault, "broken.md", "---\ntags: [unclosed\n---\nBody.\n")
	put(t, vault, "shot.png", "png")
	put(t, vault, ".obsidian/app.json", "{}")

	rep, err := Import(vault, brain, "acme", note.Reference, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, n := range rep.Notes {
		got[n.From] = n.To + n.Skipped
	}
	if got["Daily Plan.md"] != "projects/acme/daily-plan.md" || got["research/Other Note.md"] != "projects/acme/other-note.md" ||
		got["research/existing.md"] != "projects/acme/existing.mdthe brain already has projects/acme/existing.md" || !strings.HasPrefix(got["broken.md"], "properties do not parse") {
		t.Errorf("plan = %v", got)
	}
	if strings.Join(rep.Attachments, ",") != "shot.png" || len(rep.Missing) != 1 || !strings.Contains(rep.Missing[0], "[[Ghost]]") || rep.IndexLines != 3 {
		t.Errorf("report = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(brain, "projects/acme/daily-plan.md")); err == nil {
		t.Fatal("a dry run wrote a note")
	}

	if _, err := Import(vault, brain, "acme", note.Reference, false); err != nil {
		t.Fatal(err)
	}
	daily, err := note.Load(filepath.Join(brain, "projects/acme/daily-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	if daily.Name != "Daily Plan" || daily.Description != "Plan for the launch" || strings.Join(daily.Tags, ",") != "launch,project/alpha" || daily.Scope != "project:acme" || daily.Kind != note.Reference {
		t.Errorf("daily = %+v", daily)
	}
	other := read(t, filepath.Join(brain, "projects/acme/other-note.md"))
	for _, want := range []string{"description: Notes from the user interviews", "status: done", "tags: [research]", "aliases: [Other Note]", "Interviews went well."} {
		if !strings.Contains(other, want) {
			t.Errorf("other-note lacks %q:\n%s", want, other)
		}
	}
	if index := read(t, filepath.Join(brain, "projects/acme/MEMORY.md")); !strings.Contains(index, "[Daily Plan](daily-plan.md) — Plan for the launch") {
		t.Errorf("MEMORY.md = %s", index)
	}
	if read(t, filepath.Join(brain, "projects/acme/existing.md")) != "---\nname: existing\ndescription: already here\nkind: reference\nscope: project:acme\n---\nHere.\n" {
		t.Error("the import wrote over a note of the brain")
	}
}
