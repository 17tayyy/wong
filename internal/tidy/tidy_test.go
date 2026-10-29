package tidy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func write(t *testing.T, brain, rel, kind, name, description, body string) {
	t.Helper()
	path := filepath.Join(brain, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\nkind: " + kind + "\n---\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicates(t *testing.T) {
	brain := t.TempDir()
	write(t, brain, "projects/acme/no-trailers.md", "preference", "no-trailers",
		"Never add Co-Authored-By trailers to git commits", "Commits go without Co-Authored-By or Generated-with lines.")
	write(t, brain, "projects/shop/commit-style.md", "preference", "commit-style",
		"Commits with no Co-Authored-By trailers at all", "No Co-Authored-By, no Generated-with line in git commits.")
	write(t, brain, "projects/shop/use-uv.md", "preference", "use-uv",
		"Run Python through uv", "Always uv run pytest, never a bare python3.")
	write(t, brain, "projects/acme/billing.md", "project", "billing",
		"Never add Co-Authored-By trailers to git commits", "A project note is not a preference, even if it reads the same.")

	notes, err := Load(brain)
	if err != nil {
		t.Fatal(err)
	}
	got := Duplicates(notes)

	want := [][]string{{"projects/acme/no-trailers.md", "projects/shop/commit-style.md"}}
	if len(got) != 1 || !slices.Equal(got[0], want[0]) {
		t.Errorf("Duplicates() = %v, want %v", got, want)
	}
}

func TestMisplaced(t *testing.T) {
	brain := t.TempDir()
	write(t, brain, "projects/acme/blog.md", "project", "blog",
		"The personal blog lives in side-project", "side-project is Astro; posts for side.project go in English. side-project deploys alone.")
	write(t, brain, "projects/acme/billing.md", "project", "billing",
		"Billing in acme-api", "acme-api bills through Stripe; the side-project has nothing to do with it.")
	write(t, brain, "me/queue.md", "preference", "queue",
		"Use side-project as the task queue", "side-project everywhere; side-project is the queue for acme too.")
	write(t, brain, "me/orca.md", "reference", "orca",
		"How acme runs on Orca", "acme-api and acme-web each run in an Orca worktree for acme.")
	names := map[string][]string{"acme": {"acme", "acme-api", "acme-web"}, "side-project": {"side-project", "side.project"}}

	notes, err := Load(brain)
	if err != nil {
		t.Fatal(err)
	}
	got := Misplaced(notes, names)

	want := []Move{
		{Note: "me/orca.md", To: "projects/acme"},
		{Note: "projects/acme/blog.md", To: "projects/side-project"},
	}
	if len(got) != len(want) {
		t.Fatalf("Misplaced() = %+v", got)
	}
	for i := range want {
		if got[i].Note != want[i].Note || got[i].To != want[i].To {
			t.Errorf("move %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGrowing(t *testing.T) {
	brain := t.TempDir()
	log := "PRE starts on deploy.\n\n**Other side (2026-09-30):** deploying both stalls.\n\n**Measured 2026-10-10:** startup takes 2 minutes.\n\n" +
		"Some plain paragraph.\n\n**2026-10-23:** the user said it starts by itself.\n\nStatus 2026-10-24: superseded above. It mentions 2026-01-01 later on, far from its start, which does not count."
	write(t, brain, "projects/acme/pre-alarm.md", "project", "pre-alarm", "PRE goes idle", log)
	write(t, brain, "projects/acme/history.md", "decision", "history", "Decided once", "Decided on 2026-10-08.\n\nWhy: it is cheaper.\n\nSee 2026-10-09 notes.")
	notes, err := Load(brain)
	if err != nil {
		t.Fatal(err)
	}
	got := Growing(notes)
	if len(got) != 1 || got[0].Note != filepath.Join("projects", "acme", "pre-alarm.md") || got[0].Dated != 4 || got[0].Parts != 6 || got[0].First != "2026-01-01" || got[0].Last != "2026-10-24" {
		t.Errorf("Growing = %+v", got)
	}
}

func TestIdle(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, -6, 0)
	for name, kind := range map[string]string{"q3-migration": "project", "use-uv": "preference", "fresh": "project"} {
		path := filepath.Join(dir, "projects", "acme", name+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: d\nkind: "+kind+"\n---\nBody.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if name != "fresh" {
			os.Chtimes(path, old, old)
		}
	}
	notes, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	idle := Idle(notes, time.Now().AddDate(0, 0, -120))
	if len(idle) != 1 || idle[0].Name != "q3-migration" {
		t.Errorf("Idle = %v", idle)
	}
}
