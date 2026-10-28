package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/remote"
)

const acmeIndex = "- [deploy-runbook](deploy-runbook.md) — How a release goes out\n- [search](search.md) — Search rewrite\n"

func (b browser) send(path string, form url.Values, header map[string]string) (int, map[string]string, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.srv.URL)
	req.Header.Set("Accept", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]string
	json.Unmarshal(body, &out)
	return resp.StatusCode, out, string(body)
}

func file(t *testing.T, store *remote.Store, path string) string {
	t.Helper()
	files, err := store.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return string(f.Content)
		}
	}
	return ""
}

func withIndex(t *testing.T, store *remote.Store) {
	t.Helper()
	up := remote.Upload{Path: "projects/acme/MEMORY.md", Sha: remote.Sha([]byte(acmeIndex)), Content: []byte(acmeIndex)}
	if _, _, err := store.Apply("desktop", []remote.Upload{up}); err != nil {
		t.Fatal(err)
	}
}

func TestEditingABlock(t *testing.T) {
	store, b := signedIn(t)
	doc := "---\nname: notes\ndescription: Long notes\nkind: reference\nscope: global\n---\n\nFirst part.\n\nSecond part.\n"
	if _, _, err := store.Apply("desktop", []remote.Upload{{Path: "me/notes.md", Sha: remote.Sha([]byte(doc)), Content: []byte(doc)}}); err != nil {
		t.Fatal(err)
	}
	_, page := b.get("/notes/me/notes.md")
	if !strings.Contains(page, `<div class="blk" data-from="3" data-to="4" data-md="Second part.">`) || !strings.Contains(page, "data-blocks") {
		t.Fatalf("note page has no editable blocks:\n%s", page)
	}

	code, out, raw := b.send("/blocks/me/notes.md", url.Values{"base": {remote.Sha([]byte(doc))}, "from": {"3"}, "to": {"4"}, "text": {"Second part, **edited**.\n\nA third."}}, nil)
	if code != http.StatusOK || !strings.Contains(out["html"], "<strong>edited</strong>") || !strings.Contains(out["html"], `data-from="5"`) {
		t.Fatalf("saving a block: %d %s", code, raw)
	}
	want := "---\nname: notes\ndescription: Long notes\nkind: reference\nscope: global\n---\n\nFirst part.\n\nSecond part, **edited**.\n\nA third.\n"
	if got := file(t, store, "me/notes.md"); got != want || out["sha"] != remote.Sha([]byte(want)) {
		t.Errorf("after saving a block = %q", got)
	}
	if code, _, _ := b.send("/blocks/me/notes.md", url.Values{"base": {remote.Sha([]byte(doc))}, "from": {"1"}, "to": {"2"}, "text": {"x"}}, nil); code != http.StatusConflict {
		t.Errorf("saving over a newer version: %d", code)
	}
	if code, _, _ := b.send("/blocks/me/notes.md", url.Values{"base": {out["sha"]}, "from": {"-1"}, "to": {"-1"}, "text": {"The end."}}, nil); code != http.StatusOK || !strings.HasSuffix(file(t, store, "me/notes.md"), "A third.\n\nThe end.\n") {
		t.Errorf("appending a block: %d %q", code, file(t, store, "me/notes.md"))
	}
	if code, out, _ := b.send("/notes/me/notes.md", url.Values{"base": {remote.Sha([]byte(file(t, store, "me/notes.md")))}, "content": {doc}}, nil); code != http.StatusOK || out["sha"] != remote.Sha([]byte(doc)) {
		t.Errorf("saving the Markdown view: %d %v", code, out)
	}
}

func TestDescriptionFollowsIntoTheIndex(t *testing.T) {
	store, b := signedIn(t)
	withIndex(t, store)
	code, _, raw := b.send("/description/projects/acme/deploy-runbook.md", url.Values{"base": {remote.Sha([]byte(runbook))}, "description": {"How Acme ships"}}, nil)
	if code != http.StatusOK {
		t.Fatalf("description: %d %s", code, raw)
	}
	if !strings.Contains(file(t, store, "projects/acme/deploy-runbook.md"), "description: How Acme ships") || !strings.Contains(file(t, store, "projects/acme/MEMORY.md"), "(deploy-runbook.md) — How Acme ships") {
		t.Error("the description did not change in the note and its index line")
	}
}

func TestRenameUpdatesIndexAndLinksAndUndoesAsOne(t *testing.T) {
	store, b := signedIn(t)
	withIndex(t, store)

	if code, _, _ := b.send("/rename/projects/acme/deploy-runbook.md", url.Values{"name": {"search"}}, nil); code != http.StatusConflict {
		t.Errorf("renaming onto another note: %d", code)
	}
	code, out, raw := b.send("/rename/projects/acme/deploy-runbook.md", url.Values{"name": {"release-runbook"}}, nil)
	if code != http.StatusOK || out["url"] != "/notes/projects/acme/release-runbook.md" || out["op"] == "" {
		t.Fatalf("rename: %d %s", code, raw)
	}
	if file(t, store, "projects/acme/deploy-runbook.md") != "" || !strings.Contains(file(t, store, "projects/acme/release-runbook.md"), "name: release-runbook") {
		t.Error("the note was not renamed")
	}
	if got := file(t, store, "projects/acme/MEMORY.md"); !strings.HasPrefix(got, "- [release-runbook](release-runbook.md) — How a release goes out\n") {
		t.Errorf("MEMORY.md = %q", got)
	}
	if !strings.Contains(file(t, store, "projects/acme/search.md"), "See [[release-runbook]].") {
		t.Error("the link in search was not updated")
	}
	_, history := b.get("/history")
	if strings.Count(history, ">renamed<") != 1 || !strings.Contains(history, "+3 files") {
		t.Errorf("history does not show the rename as one change:\n%s", index(history))
	}

	code, _, raw = b.send("/ops/"+out["op"]+"/undo", nil, map[string]string{"Referer": b.srv.URL + out["url"]})
	if code != http.StatusOK || !strings.Contains(raw, `/notes/projects/acme/deploy-runbook.md?done=reverted`) {
		t.Fatalf("undo: %d %s", code, raw)
	}
	if file(t, store, "projects/acme/release-runbook.md") != "" || file(t, store, "projects/acme/deploy-runbook.md") != runbook || file(t, store, "projects/acme/MEMORY.md") != acmeIndex || !strings.Contains(file(t, store, "projects/acme/search.md"), "[[deploy-runbook]]") {
		t.Error("undoing the rename did not bring every file back")
	}
	if code, _, _ := b.send("/ops/"+out["op"]+"/undo", nil, nil); code != http.StatusConflict {
		t.Errorf("undoing twice: %d", code)
	}
}

func TestMoveAndDelete(t *testing.T) {
	store, b := signedIn(t)
	withIndex(t, store)

	code, out, raw := b.send("/move/me/use-uv.md", url.Values{"to": {"acme"}}, nil)
	if code != http.StatusOK || out["url"] != "/notes/projects/acme/use-uv.md" {
		t.Fatalf("move: %d %s", code, raw)
	}
	if moved := file(t, store, "projects/acme/use-uv.md"); !strings.Contains(moved, "scope: project:acme") || file(t, store, "me/use-uv.md") != "" {
		t.Errorf("moved note = %q", moved)
	}
	if !strings.Contains(file(t, store, "projects/acme/MEMORY.md"), "[use-uv](use-uv.md)") {
		t.Error("the note's line did not reach the new shelf's MEMORY.md")
	}
	if code, _, _ := b.send("/move/projects/acme/use-uv.md", url.Values{"to": {"../etc"}}, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("moving out of the brain: %d", code)
	}

	code, out, raw = b.send("/delete/projects/acme/search.md", nil, nil)
	if code != http.StatusOK || out["op"] == "" {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if file(t, store, "projects/acme/search.md") != "" || strings.Contains(file(t, store, "projects/acme/MEMORY.md"), "search.md") {
		t.Error("the note or its index line is still there")
	}
	_, page := b.get("/?done=deleted&op=" + out["op"])
	if !strings.Contains(page, `action="/ops/`+out["op"]+`/undo"`) || !strings.Contains(page, "Deleted “search”. It stays in the history.") {
		t.Error("the library does not offer to undo the deletion")
	}
	if code, _ := b.post("/ops/"+out["op"]+"/undo", nil, b.srv.URL); code != http.StatusOK || !strings.Contains(file(t, store, "projects/acme/MEMORY.md"), "search.md") {
		t.Errorf("undoing the deletion: %d", code)
	}
	if code, _ := b.post("/ops/rename:zzzz/undo", nil, b.srv.URL); code != http.StatusNotFound {
		t.Errorf("a made-up operation: %d", code)
	}
}

func TestOneStatusPillPerNote(t *testing.T) {
	store, b := signedIn(t)
	findings := []check.Finding{
		{Note: "projects/acme/deploy-runbook.md", Severity: check.Review, Ref: "src/a.go", Message: "a.go changed"},
		{Note: "projects/acme/deploy-runbook.md", Severity: check.Review, Ref: "src/b.go", Message: "b.go changed"},
	}
	if err := store.ReportFindings("laptop", "acme", findings); err != nil {
		t.Fatal(err)
	}
	_, page := b.get("/")
	if !strings.Contains(page, `<span class="pill yellow">Review<span class="n">2</span></span>`) || strings.Count(page, ">Review<") != 1 {
		t.Errorf("library status:\n%s", index(page))
	}
	_, page = b.get("/notes/projects/acme/deploy-runbook.md")
	if strings.Count(page, `class="callout`) != 1 || !strings.Contains(page, "· 2 findings") || !strings.Contains(page, "b.go changed") {
		t.Errorf("the note page does not gather its findings in one callout:\n%s", page)
	}
}
