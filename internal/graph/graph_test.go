package graph

import (
	"slices"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/note"
)

func mk(path, name, desc, body string, tags ...string) Note {
	n := note.New(name, desc, note.Reference, "", body)
	n.Tags = tags
	return Note{Path: path, Note: n}
}

func sample() *Graph {
	return New([]Note{
		mk("projects/acme/deploy-runbook.md", "deploy-runbook", "How a release goes out", "Merge to `main`, then promote. Tracked in ENG-15.\n", "deploy", "ci"),
		mk("projects/acme/rate-limits.md", "Rate limits", "Per API key, not per IP", "Decided on 2026-10-08 in ENG-15.\n", "api"),
		mk("projects/acme/staging.md", "staging", "Staging restores nightly", "See [[deploy-runbook]].\n", "deploy"),
		mk("me/use-uv.md", "use-uv", "uv, never pip", "Use uv for Python packages.\n", "python"),
		mk("projects/other/notes.md", "notes", "Release notes of the other product", "How a release goes out there.\n"),
	})
}

func TestCheckTags(t *testing.T) {
	g := sample()
	got, err := g.CheckTags([]string{"#Deploy", "API", "security", "deploy"})
	if err != nil || strings.Join(got, ",") != "deploy,api,security" {
		t.Errorf("CheckTags = %v, %v", got, err)
	}
	if _, err := g.CheckTags([]string{"deployment"}); err == nil || !strings.Contains(err.Error(), `use "deploy"`) {
		t.Errorf("a spelling of a tag in use: %v", err)
	}
	if _, err := g.CheckTags([]string{"caching", "cache"}); err == nil || !strings.Contains(err.Error(), "keep one") {
		t.Errorf("two spellings of one new tag: %v", err)
	}
	if list := g.TagList(2); strings.Join(list, ",") != "deploy (2),api (1)" {
		t.Errorf("TagList = %v", list)
	}
}

func TestMissingLinks(t *testing.T) {
	g := sample()
	missing := g.Missing("See [[deploy-runbok]], [[Rate limits]], [[staging|the stage]] and [[gone]], written `[[name]]`.\n\n```\n[[example]]\n```\n", "Was [[gone]] before.")
	if len(missing) != 1 || !strings.Contains(missing[0], "[[deploy-runbok]] reaches no note; did you mean [[deploy-runbook]]") {
		t.Errorf("Missing = %q", missing)
	}
}

func TestSimilarAndRelated(t *testing.T) {
	g := sample()
	dup := mk("projects/acme/release-flow.md", "release-flow", "How a release goes out to production", "Merge, then promote.\n")
	similar := g.Similar(dup)
	if len(similar) != 1 || similar[0].Path != "projects/acme/deploy-runbook.md" {
		t.Errorf("Similar = %v", similar)
	}
	if fresh := g.Similar(mk("projects/acme/oncall.md", "oncall", "Who answers the pager at night", "Rotation weekly.\n")); len(fresh) != 0 {
		t.Errorf("a new subject looked like %v", fresh)
	}
	related := g.Related(mk("projects/acme/search.md", "search", "Search rewrite", "Tracked in ENG-15. Links [[staging]].\n", "deploy"), 5)
	if len(related) != 2 || !strings.HasPrefix(related[0], "[[deploy-runbook]]") || !strings.Contains(related[0], "ENG-15, #deploy") || !strings.HasPrefix(related[1], "[[Rate limits]]") {
		t.Errorf("Related = %q", related)
	}
}

func TestSearch(t *testing.T) {
	g := New([]Note{
		mk("projects/acme/migracion.md", "migracion", "La migración de la base de datos", "Se hizo con Alembic.\nCorre antes del despliegue.\n"),
		mk("projects/acme/deploy.md", "deploy", "How Acme ships", "The migración runs first.\n", "database"),
		mk("projects/acme/other.md", "other", "Unrelated", "Nothing here.\n"),
		mk("projects/side/migrate.md", "migrate", "Side project migration", "migracion too.\n"),
		mk("me/style.md", "style", "Migraciones con nombre claro", "Always name them.\n"),
	})
	var got []string
	for _, h := range g.Search("MIGRACION", "acme") {
		got = append(got, h.Path)
	}
	if strings.Join(got, ",") != "me/style.md,projects/acme/migracion.md,projects/acme/deploy.md" {
		t.Errorf("Search = %v", got)
	}
	hits := g.Search("database migración", "")
	if len(hits) != 1 || hits[0].Path != "projects/acme/deploy.md" || hits[0].Line != "The migración runs first." {
		t.Errorf("Search across fields = %+v", hits)
	}
	if g.Search("   ", "") != nil || len(g.Search("migracion nothing", "")) != 0 {
		t.Error("an empty query or one no note holds found notes")
	}
}

func TestArchivedNotesStayApart(t *testing.T) {
	g := New([]Note{
		{Path: "projects/acme/a.md", Note: &note.Note{Name: "a", Description: "billing today", Body: "See [[old-billing]]."}},
	}).WithArchive([]Note{
		{Path: "projects/acme/archive/old-billing.md", Note: &note.Note{Name: "old-billing", Description: "billing before Stripe"}},
	})
	if p, ok := g.Resolve("old-billing"); !ok || p != "projects/acme/archive/old-billing.md" {
		t.Errorf("Resolve of an archived note = %s, %v", p, ok)
	}
	if missing := g.Missing("See [[old-billing]].", ""); len(missing) != 0 {
		t.Errorf("a link to an archived note is missing: %v", missing)
	}
	if hits := g.Search("billing", "acme"); len(hits) != 1 || hits[0].Path != "projects/acme/a.md" {
		t.Errorf("Search = %v", hits)
	}
	if hits := g.SearchArchive("stripe", "acme"); len(hits) != 1 || hits[0].Shelf() != "acme" {
		t.Errorf("SearchArchive = %v", hits)
	}
}

func TestSuperseded(t *testing.T) {
	old := mk("projects/acme/limits-per-ip.md", "limits-per-ip", "Rate limits per IP", "Per IP.")
	gone := mk("projects/acme/archive/limits-v0.md", "limits-v0", "First limits", "None.")
	fresh := mk("projects/acme/limits-per-key.md", "limits-per-key", "Rate limits per key", "Per key.")
	fresh.Supersedes = []string{"Rate limits per IP", "limits-per-ip", "limits-v0", "nowhere"}
	old.Supersedes = []string{"limits-v0"}
	g := New([]Note{old, fresh}).WithArchive([]Note{gone})
	if by := g.SupersededBy("projects/acme/limits-per-ip.md"); !slices.Equal(by, []string{"projects/acme/limits-per-key.md"}) {
		t.Errorf("SupersededBy = %q", by)
	}
	if by := g.SupersededBy("projects/acme/archive/limits-v0.md"); !slices.Equal(by, []string{"projects/acme/limits-per-ip.md", "projects/acme/limits-per-key.md"}) {
		t.Errorf("an archived note: %q", by)
	}
	if got := g.UnknownSupersedes(fresh.Note); !slices.Equal(got, []string{"it supersedes [[Rate limits per IP]], which reaches no note", "it supersedes [[nowhere]], which reaches no note"}) {
		t.Errorf("UnknownSupersedes = %q", got)
	}
}
