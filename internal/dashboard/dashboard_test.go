package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/remote"
)

type browser struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
}

func (b browser) get(path string) (int, string) {
	b.t.Helper()
	resp, err := b.client.Get(b.srv.URL + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (b browser) post(path string, form url.Values, origin string) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

const runbook = "---\nname: deploy-runbook\ndescription: How a release goes out\nkind: reference\nscope: project:acme\n---\nMerge to `main`, then promote.\n"

func setup(t *testing.T) (*remote.Store, browser, string) {
	t.Helper()
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for path, content := range map[string]string{
		"projects/acme/deploy-runbook.md": runbook,
		"projects/acme/search.md":         "---\nname: search\ndescription: Search rewrite\nkind: project\nscope: project:acme\n---\nTracked in ENG-15, in progress. See [[deploy-runbook]].\n",
		"me/use-uv.md":                    "---\nname: use-uv\ndescription: uv, never pip\nkind: preference\nscope: global\n---\nUse uv.\n",
	} {
		if _, _, err := store.Apply("desktop", []remote.Upload{{Path: path, Sha: remote.Sha([]byte(content)), Content: []byte(content)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReportFindings("desktop", "acme", []check.Finding{{Note: "projects/acme/search.md", Severity: check.Stale, Ref: "ENG-15", Message: `Done since 2026-10-20, but the note says "in progress"`}}); err != nil {
		t.Fatal(err)
	}
	token, err := store.NewToken("browser")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Devices: remote.NewDevices(), SignIns: remote.NewSignIns()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return store, browser{t, srv, &http.Client{Jar: jar}}, token
}

func TestSignIn(t *testing.T) {
	_, b, token := setup(t)

	if _, body := b.get("/"); !strings.Contains(body, "Sign in to wong") {
		t.Fatalf("an anonymous visit was not sent to sign in:\n%s", body)
	}
	if code, body := b.post("/login", url.Values{"token": {"wong_wrong"}}, b.srv.URL); code != http.StatusUnauthorized || !strings.Contains(body, "not one this server knows") {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := b.post("/login", url.Values{"token": {token}}, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-site sign in: %d", code)
	}
	if code, body := b.post("/login", url.Values{"token": {token}, "next": {"//evil.example"}}, b.srv.URL); code != http.StatusOK || !strings.Contains(body, "<h1>Library</h1>") {
		t.Errorf("sign in did not land on the library: %d", code)
	}
}

func signedIn(t *testing.T) (*remote.Store, browser) {
	store, b, token := setup(t)
	if code, _ := b.post("/login", url.Values{"token": {token}}, b.srv.URL); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}
	return store, b
}

func TestLibraryAndNote(t *testing.T) {
	_, b := signedIn(t)

	_, body := b.get("/")
	for _, want := range []string{`id="acme"`, `id="me"`, "deploy-runbook", `<span class="pill red">Stale</span>`, "1 note needs a correction", `title="Done since 2026-10-20, but the note says &#34;in progress&#34; "`} {
		if !strings.Contains(body, want) {
			t.Errorf("library lacks %q", want)
		}
	}
	if _, body := b.get("/?show=pending"); strings.Contains(index(body), "deploy-runbook") || !strings.Contains(index(body), ">search<") {
		t.Error("the pending filter kept a note with no correction")
	}
	if _, body := b.get("/?q=uv"); !strings.Contains(index(body), "use-uv") || strings.Contains(index(body), ">search<") {
		t.Error("the search did not narrow the library")
	}

	_, body = b.get("/notes/projects/acme/search.md")
	for _, want := range []string{`<a href="/notes/projects/acme/deploy-runbook.md">deploy-runbook</a>`, "This note contradicts its source.", `History<span class="count">1</span>`, `Linked mentions<span class="count">0</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("note page lacks %q", want)
		}
	}
	if code, _ := b.get("/notes/projects/acme/nothing.md"); code != http.StatusNotFound {
		t.Errorf("missing note: %d", code)
	}
}

func TestEditAndUndo(t *testing.T) {
	store, b := signedIn(t)
	edited := strings.Replace(runbook, "then promote", "then promote from the release workflow", 1)

	code, _ := b.post("/notes/projects/acme/deploy-runbook.md", url.Values{"content": {edited}, "base": {remote.Sha([]byte(runbook))}}, "https://evil.example")
	if code != http.StatusForbidden {
		t.Errorf("cross-site save: %d", code)
	}
	code, body := b.post("/notes/projects/acme/deploy-runbook.md", url.Values{"content": {edited}, "base": {remote.Sha([]byte(runbook))}}, b.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "Saved.") {
		t.Fatalf("save: %d", code)
	}
	log, _ := store.Log(1, 0)
	if log[0].Machine != "dashboard:browser" || log[0].Path != "projects/acme/deploy-runbook.md" {
		t.Errorf("latest change = %+v", log[0])
	}
	if code, body := b.post("/notes/projects/acme/deploy-runbook.md", url.Values{"content": {runbook}, "base": {remote.Sha([]byte(runbook))}}, b.srv.URL); code != http.StatusConflict || !strings.Contains(body, "changed since you opened it") {
		t.Errorf("save over a newer version: %d", code)
	}
	if code, body := b.post("/notes/projects/acme/deploy-runbook.md", url.Values{"content": {"no frontmatter"}, "base": {remote.Sha([]byte(edited))}}, b.srv.URL); code != http.StatusConflict || !strings.Contains(body, "did not save") {
		t.Errorf("save of a broken note: %d", code)
	}

	_, body = b.get("/changes/" + itoa(log[0].Seq))
	if !strings.Contains(body, `<ins><span class="sign">+</span>Merge to `+"`main`"+`, then promote from the release workflow.</ins>`) || !strings.Contains(body, "you, in the dashboard") {
		t.Errorf("change page:\n%s", body)
	}
	if code, body := b.post("/changes/"+itoa(log[0].Seq)+"/undo", nil, b.srv.URL); code != http.StatusOK || !strings.Contains(body, "Undone.") {
		t.Fatalf("undo: %d", code)
	}
	files, _ := store.Files()
	for _, f := range files {
		if f.Path == "projects/acme/deploy-runbook.md" && string(f.Content) != runbook {
			t.Errorf("after undo = %q", f.Content)
		}
	}
	if code, body := b.post("/changes/"+itoa(log[0].Seq)+"/undo", nil, b.srv.URL); code != http.StatusOK || !strings.Contains(body, "A later change to this file exists") {
		t.Errorf("undoing a replaced change: %d", code)
	}
}

// index cuts a library page down to its columns of notes.
func index(page string) string {
	_, cols, _ := strings.Cut(page, `<nav class="tabs"`)
	cols, _, _ = strings.Cut(cols, "</main>")
	return cols
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestNewNote(t *testing.T) {
	store, b := signedIn(t)
	form := url.Values{"folder": {"acme"}, "kind": {"decision"}, "name": {"Rate limits per tenant"},
		"description": {"Per API key, not per IP"}, "body": {"Tracked in ENG-40."}}

	code, body := b.post("/new", form, b.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "Created, and added to its folder") {
		t.Fatalf("create: %d", code)
	}
	files, _ := store.Files()
	var index, created string
	for _, f := range files {
		switch f.Path {
		case "projects/acme/MEMORY.md":
			index = string(f.Content)
		case "projects/acme/rate-limits-per-tenant.md":
			created = string(f.Content)
		}
	}
	if !strings.Contains(created, "kind: decision") || !strings.Contains(created, "scope: project:acme") || !strings.Contains(created, "Tracked in ENG-40.") {
		t.Errorf("created note:\n%s", created)
	}
	if index != "- [Rate limits per tenant](rate-limits-per-tenant.md) — Per API key, not per IP\n" {
		t.Errorf("MEMORY.md = %q", index)
	}
	if code, body := b.post("/new", form, b.srv.URL); code != http.StatusUnprocessableEntity || !strings.Contains(body, "There is already a note") {
		t.Errorf("creating it twice: %d", code)
	}
	form.Set("folder", "../etc")
	if code, _ := b.post("/new", form, b.srv.URL); code != http.StatusUnprocessableEntity {
		t.Errorf("a folder outside the brain: %d", code)
	}
}

func TestGraph(t *testing.T) {
	_, b := signedIn(t)
	_, page := b.get("/graph")
	if !strings.Contains(page, `data-graph="{&#34;nodes&#34;:[`) || !strings.Contains(page, "3 notes · 1 links") {
		t.Fatalf("graph page:\n%s", page)
	}
	start := strings.Index(page, `data-graph="`) + len(`data-graph="`)
	raw := strings.NewReplacer("&#34;", `"`).Replace(page[start : start+strings.Index(page[start:], `"`)])
	var data graphData
	if err := json.Unmarshal([]byte(raw), &data); err != nil || len(data.Nodes) != 3 || len(data.Edges) != 1 {
		t.Fatalf("graph data %s: %v", raw, err)
	}
	a, z := data.Nodes[data.Edges[0][0]].Path, data.Nodes[data.Edges[0][1]].Path
	if a+" "+z != "projects/acme/deploy-runbook.md projects/acme/search.md" || data.Nodes[data.Edges[0][1]].State != "stale" {
		t.Errorf("edge %s - %s, nodes %+v", a, z, data.Nodes)
	}
	if _, note := b.get("/notes/projects/acme/search.md"); !strings.Contains(note, `class="graph local"`) || !strings.Contains(note, "graph.js") {
		t.Error("the note page has no local graph")
	}
	if _, note := b.get("/notes/me/use-uv.md"); strings.Contains(note, `class="graph local"`) {
		t.Error("a note without links got a local graph")
	}
}

func TestEachTokenSignsInToItsBrain(t *testing.T) {
	folder, err := remote.OpenFolder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	tokens := map[string]string{}
	for brain, name := range map[string]string{"alice": "deploy-runbook", "bob": "use-uv"} {
		store, _, err := folder.Create(brain)
		if err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: A note of " + brain + "\nkind: reference\nscope: global\n---\nBody.\n"
		if _, _, err := store.Apply("desktop", []remote.Upload{{Path: "me/" + name + ".md", Sha: remote.Sha([]byte(content)), Content: []byte(content)}}); err != nil {
			t.Fatal(err)
		}
		if tokens[brain], err = store.NewToken("browser"); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(folder, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Devices: remote.NewDevices(), SignIns: remote.NewSignIns()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, srv, &http.Client{Jar: jar}}

	code, body := b.post("/login", url.Values{"token": {tokens["bob"]}}, srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "use-uv") || strings.Contains(body, "deploy-runbook") {
		t.Fatalf("bob's library shows another brain or none: %d", code)
	}
	if !strings.Contains(body, `<span class="ws-host">bob</span>`) {
		t.Error("the sidebar does not name the brain")
	}
	if _, body := b.get("/notes/me/deploy-runbook.md"); strings.Contains(body, "A note of alice") {
		t.Error("bob's session opened a note of alice's brain")
	}
}

func TestWebhooksPage(t *testing.T) {
	folder, err := remote.OpenFolder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	store, _, err := folder.Create("bob")
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.NewToken("browser")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(folder, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Webhooks: remote.Webhooks{GitHubSecret: "the server's"}, Devices: remote.NewDevices(), SignIns: remote.NewSignIns()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, srv, &http.Client{Jar: jar}}
	if code, _ := b.post("/login", url.Values{"token": {token}}, srv.URL); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}

	_, body := b.get("/webhooks")
	if !strings.Contains(body, srv.URL+"/hooks/bob/github") || !strings.Contains(body, srv.URL+"/hooks/bob/linear") || strings.Contains(body, "WONG_GITHUB_SECRET") || strings.Count(body, `<span class="pill gray">Off</span>`) != 2 {
		t.Fatalf("an account's webhooks page:\n%s", body)
	}
	if code, body := b.post("/webhooks/github", url.Values{"do": {"generate"}}, srv.URL); code != http.StatusOK || !strings.Contains(body, "A new secret is ready") {
		t.Errorf("make a secret: %d", code)
	}
	_, body = b.get("/webhooks")
	if secret, _ := store.HookSecret("github"); len(secret) != 64 || !strings.Contains(body, "<code>"+secret+"</code>") {
		t.Errorf("the secret made is not kept and shown: %q", secret)
	}
	if code, _ := b.post("/webhooks/linear", url.Values{"do": {"save"}, "secret": {" lin_wh_abc \n"}}, srv.URL); code != http.StatusOK {
		t.Errorf("save Linear's secret: %d", code)
	}
	if secret, _ := store.HookSecret("linear"); secret != "lin_wh_abc" {
		t.Errorf("Linear's secret = %q", secret)
	}
	if code, _ := b.post("/webhooks/linear", url.Values{"do": {"off"}}, srv.URL); code != http.StatusOK {
		t.Errorf("turn off: %d", code)
	}
	if secret, _ := store.HookSecret("linear"); secret != "" {
		t.Errorf("Linear's webhook is still on: %q", secret)
	}
	if code, _ := b.post("/webhooks/gitlab", url.Values{"do": {"generate"}}, srv.URL); code != http.StatusNotFound {
		t.Errorf("a webhook wong does not take: %d", code)
	}
	if code, _ := b.post("/webhooks/github", url.Values{"do": {"off"}}, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("a cross-site post: %d", code)
	}
}

func TestWebhooksPageOfASingleBrain(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	token, err := store.NewToken("browser")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Webhooks: remote.Webhooks{GitHubSecret: "the server's"}, Devices: remote.NewDevices(), SignIns: remote.NewSignIns()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, srv, &http.Client{Jar: jar}}
	if code, _ := b.post("/login", url.Values{"token": {token}}, srv.URL); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}

	_, body := b.get("/webhooks")
	if !strings.Contains(body, srv.URL+"/hooks/github") || !strings.Contains(body, "<code>WONG_GITHUB_SECRET</code>") || strings.Contains(body, `action="/webhooks/github"`) || !strings.Contains(body, `action="/webhooks/linear"`) {
		t.Errorf("a single brain's webhooks page:\n%s", body)
	}
}

func TestConnectAMachine(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	token, err := store.NewToken("browser")
	if err != nil {
		t.Fatal(err)
	}
	devices := remote.NewDevices()
	h, err := Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Devices: devices, SignIns: remote.NewSignIns()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, srv, &http.Client{Jar: jar}}
	if code, _ := b.post("/login", url.Values{"token": {token}}, srv.URL); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}

	if code, body := b.post("/device", url.Values{"code": {"BCDF-GHJK"}, "do": {"find"}}, srv.URL); code != http.StatusNotFound || !strings.Contains(body, "No machine is waiting") {
		t.Errorf("an unknown code: %d", code)
	}
	secret, code, err := devices.Ask("browser", "192.0.2.7")
	if err != nil {
		t.Fatal(err)
	}
	typed := strings.ToLower(remote.ShowCode(code))
	_, body := b.post("/device", url.Values{"code": {typed}, "do": {"find"}}, srv.URL)
	if !strings.Contains(body, "192.0.2.7") || !strings.Contains(body, "already has a token") || !strings.Contains(body, "Connect and replace") {
		t.Fatalf("a machine asking for a name in use:\n%s", body)
	}
	_, body = b.post("/device", url.Values{"code": {typed}, "do": {"approve"}, "name": {"browser"}}, srv.URL)
	if !strings.Contains(body, "Connect and replace") {
		t.Error("a name in use was replaced without saying so")
	}
	if status, _, _, _ := devices.Poll(secret); status != remote.DeviceWaiting {
		t.Errorf("the machine was let in before replacing was confirmed: %v", status)
	}
	if _, body := b.post("/device", url.Values{"code": {typed}, "do": {"approve"}, "name": {"Work Laptop"}}, srv.URL); !strings.Contains(body, "Connected. work-laptop carries on") {
		t.Errorf("approve:\n%s", body)
	}
	if status, _, token, _ := devices.Poll(secret); status != remote.DeviceApproved || token == "" {
		t.Errorf("the machine got no token: %v", status)
	}

	_, other, _ := devices.Ask("phone", "192.0.2.8")
	if _, body := b.post("/device", url.Values{"code": {other}, "do": {"deny"}}, srv.URL); !strings.Contains(body, "Turned away") {
		t.Error("deny")
	}
	if code, _ := b.post("/device", url.Values{"code": {other}, "do": {"approve"}, "name": {"phone"}}, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("a cross-site approval: %d", code)
	}

	_, last, _ := devices.Ask("tablet", "192.0.2.9")
	for range maxWrongCodes - 1 {
		b.post("/device", url.Values{"code": {"BCDF-BCDF"}, "do": {"find"}}, srv.URL)
	}
	if code, body := b.post("/device", url.Values{"code": {last}, "do": {"find"}}, srv.URL); code != http.StatusTooManyRequests || strings.Contains(body, "192.0.2.9") {
		t.Errorf("a code after too many wrong ones: %d", code)
	}
}

func TestSignInLinksAndSigningOut(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	links := remote.NewSignIns()
	h, err := Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Devices:    remote.NewDevices(),
		SignIns:    links,
		SignInURL:  "https://signup.example/login",
		AccountURL: "https://signup.example/account/",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, srv, &http.Client{Jar: jar}}

	if _, body := b.get("/login"); !strings.Contains(body, `href="https://signup.example/login"`) || !strings.Contains(body, "Sign in with a token") {
		t.Errorf("the sign-in page does not send people to the sign-up service:\n%s", body)
	}
	if _, body := b.get("/login?next=%2Fgraph"); !strings.Contains(body, `href="https://signup.example/login?next=%2Fgraph"`) {
		t.Errorf("the sign-up service is not told where to come back:\n%s", body)
	}
	code, _ := links.Issue(store, "desktop", "/")
	status, body := b.get("/signin/" + code)
	if status != http.StatusOK || !strings.Contains(body, "<h1>Library</h1>") {
		t.Fatalf("a sign-in link did not land on the library: %d", status)
	}
	if !strings.Contains(body, `href="https://signup.example/account/"`) {
		t.Error("the sidebar does not link to the service's account page")
	}
	if status, body := b.get("/signin/" + code); status != http.StatusGone || !strings.Contains(body, "used or expired") {
		t.Errorf("a link used twice: %d", status)
	}
	session := jar.Cookies(mustURL(t, srv.URL))[0].Value
	if s, _ := store.Visit(session); s.Store == nil {
		t.Fatal("the cookie holds no session")
	}
	if code, _ := b.post("/logout", url.Values{}, srv.URL); code != http.StatusOK {
		t.Errorf("sign out: %d", code)
	}
	if s, _ := store.Visit(session); s.Store != nil {
		t.Error("signing out left the session open")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestStorageShowsAgainstItsLimit(t *testing.T) {
	store, b := signedIn(t)
	if _, body := b.get("/"); strings.Contains(body, `class="storage"`) {
		t.Error("a brain without a size limit shows its storage")
	}
	store.SetLimits(remote.Limits{Size: 100 << 20})
	if _, body := b.get("/"); !strings.Contains(body, " of 100.0 MB</p>") {
		t.Errorf("the sidebar does not say how much of its room the brain takes:\n%s", body)
	}
}

func TestArchiveInTheDashboard(t *testing.T) {
	_, b := signedIn(t)
	req, _ := http.NewRequest(http.MethodPost, b.srv.URL+"/archive/projects/acme/deploy-runbook.md", nil)
	req.Header.Set("Origin", b.srv.URL)
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || out["path"] != "projects/acme/archive/deploy-runbook.md" || !strings.HasPrefix(out["op"], "archive:") {
		t.Fatalf("archive: %d %v", resp.StatusCode, out)
	}

	_, body := b.get("/")
	if !strings.Contains(body, `<details class="archived">`) || !strings.Contains(body, `<li data-note="projects/acme/archive/deploy-runbook.md"`) || strings.Contains(body, `<tr data-note="projects/acme/archive/deploy-runbook.md"`) {
		t.Errorf("the library does not keep the archived note apart:\n%s", body)
	}
	_, body = b.get("/notes/projects/acme/archive/deploy-runbook.md")
	if !strings.Contains(body, "This note is archived.") || !strings.Contains(body, "data-archived") {
		t.Errorf("the note page does not say it is archived:\n%s", body)
	}
	if code, body := b.post("/unarchive/projects/acme/archive/deploy-runbook.md", url.Values{}, b.srv.URL); code != http.StatusOK || !strings.Contains(body, "Brought “deploy-runbook” back to acme.") {
		t.Errorf("bring it back: %d", code)
	}
}

func TestConnectAnApp(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	token, _ := store.NewToken("browser")
	o, err := remote.NewOAuth(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Devices: remote.NewDevices(), SignIns: remote.NewSignIns(), OAuth: o})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(remote.OAuthHandler(o, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	resp, _ := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(`{"client_name":"Claude","redirect_uris":["https://claude.example/cb"]}`))
	var reg map[string]string
	json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()

	web := httptest.NewServer(h)
	t.Cleanup(web.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, web, &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Host == "claude.example" {
			return http.ErrUseLastResponse
		}
		return nil
	}}}
	params := url.Values{"response_type": {"code"}, "client_id": {reg["client_id"]}, "redirect_uri": {"https://claude.example/cb"},
		"state": {"s1"}, "code_challenge": {"abc"}, "code_challenge_method": {"S256"}}

	if _, body := b.get("/oauth/authorize?" + params.Encode()); !strings.Contains(body, "Sign in to wong") {
		t.Fatal("an anonymous visit was not sent to sign in first")
	}
	if code, body := b.post("/login", url.Values{"token": {token}, "next": {"/oauth/authorize?" + params.Encode()}}, web.URL); code != http.StatusOK || !strings.Contains(body, "<h1>Connect Claude</h1>") || !strings.Contains(body, "<code>claude.example</code>") {
		t.Fatalf("signing in did not come back to the approval: %d", code)
	}
	resp, err = b.client.Get(web.URL + "/oauth/authorize?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if policy := resp.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "form-action 'self' https://claude.example;") {
		t.Errorf("the approval's form cannot send the browser back to the app: %s", policy)
	}

	approve := func(do string) *url.URL {
		t.Helper()
		form := url.Values{"do": {do}}
		for k, v := range params {
			form[k] = v
		}
		req, _ := http.NewRequest(http.MethodPost, web.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", web.URL)
		resp, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		u, _ := url.Parse(resp.Header.Get("Location"))
		return u
	}
	if u := approve("deny"); u.Host != "claude.example" || u.Query().Get("error") != "access_denied" || u.Query().Get("state") != "s1" {
		t.Errorf("deny went to %v", u)
	}
	if u := approve("approve"); u.Host != "claude.example" || u.Query().Get("code") == "" || u.Query().Get("state") != "s1" {
		t.Errorf("approve went to %v", u)
	}
	bad := url.Values{"response_type": {"code"}, "client_id": {reg["client_id"]}, "redirect_uri": {"https://evil.example/cb"}, "code_challenge": {"abc"}, "code_challenge_method": {"S256"}}
	if code, body := b.get("/oauth/authorize?" + bad.Encode()); code != http.StatusBadRequest || !strings.Contains(body, "not valid") {
		t.Errorf("a redirect the client did not register: %d", code)
	}
}
