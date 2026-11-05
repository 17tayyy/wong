package remote

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/seal"
)

const appSecret = "app-secret"

// fakeGitHub is GitHub's API for one app, with the checks it was sent.
type fakeGitHub struct {
	t      *testing.T
	key    *rsa.PrivateKey
	files  []map[string]string
	mu     sync.Mutex
	checks map[int64]checkRun
	next   int64
	users  map[string][]Installation
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *GitHubApp) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{t: t, key: key, checks: map[int64]checkRun{}, users: map[string][]Installation{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	data, _ := json.Marshal(map[string]any{"id": 42, "slug": "wong-test", "name": "wong test", "client_id": "Iv1.test",
		"client_secret": "client-secret", "webhook_secret": appSecret, "pem": string(pemKey)})
	app, err := SaveGitHubApp(filepath.Join(t.TempDir(), "github-app.json"), data)
	if err != nil {
		t.Fatal(err)
	}
	app.API, app.Web = srv.URL, srv.URL
	return f, app
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/7/access_tokens":
		if !f.validJWT(auth) {
			http.Error(w, "bad jwt", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "ghs_install7", "expires_at": time.Now().Add(time.Hour)})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/acme/acme-api/pulls/5/files"):
		if auth != "ghs_install7" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		var out []map[string]string
		if r.URL.Query().Get("page") == "1" {
			out = f.files
		}
		json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/acme-api/check-runs":
		var run checkRun
		json.NewDecoder(r.Body).Decode(&run)
		f.next++
		f.checks[f.next] = run
		json.NewEncoder(w).Encode(map[string]int64{"id": f.next})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/acme/acme-api/check-runs/"):
		var id int64
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/repos/acme/acme-api/check-runs/"), &id)
		var run checkRun
		json.NewDecoder(r.Body).Decode(&run)
		old := f.checks[id]
		run.Name, run.HeadSHA = old.Name, old.HeadSHA
		f.checks[id] = run
		w.Write([]byte("{}"))
	case r.Method == http.MethodPost && r.URL.Path == "/login/oauth/access_token":
		r.ParseForm()
		if r.Form.Get("client_secret") != "client-secret" {
			json.NewEncoder(w).Encode(map[string]string{"error_description": "bad client"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "ghu_" + r.Form.Get("code")})
	case r.Method == http.MethodGet && r.URL.Path == "/user/installations":
		json.NewEncoder(w).Encode(map[string]any{"installations": f.users[auth]})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) validJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	json.Unmarshal(raw, &claims)
	return claims.Iss == "Iv1.test" && time.Unix(claims.Exp, 0).After(time.Now())
}

func (f *fakeGitHub) only() checkRun {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.checks) != 1 {
		f.t.Fatalf("checks = %+v, want one", f.checks)
	}
	return f.checks[1]
}

func appSetup(t *testing.T, app *GitHubApp) (*Store, *Links, string, machine) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	links, err := OpenLinks(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Webhooks{App: app, Links: links}))
	t.Cleanup(srv.Close)
	token, err := store.NewToken("desktop")
	if err != nil {
		t.Fatal(err)
	}
	return store, links, srv.URL, machine{brain: t.TempDir(), client: NewClient(srv.URL, token, 5*time.Second)}
}

func appDeliver(t *testing.T, url, kind, body string) int {
	t.Helper()
	return deliver(t, url+"/hooks/github-app", map[string]string{"X-GitHub-Event": kind, "X-Hub-Signature-256": "sha256=" + sign(appSecret, body)}, body)
}

const pullPayload = `{"action": "%s", "number": 5, "installation": {"id": 7},
  "pull_request": {"head": {"sha": "abc123"}}, "repository": {"full_name": "acme/acme-api"}}`

func acmeNotes(t *testing.T, brain string) {
	t.Helper()
	write(t, filepath.Join(brain, "remotes.yaml"), "acme/acme-api:\n  project: acme\n  dir: acme-api\n")
	writeAt(t, filepath.Join(brain, "projects/acme/a.md"), "---\nname: a\ndescription: d\n---\nBilling lives in `acme-api/src/billing.go`.\n", "2026-09-01")
	writeAt(t, filepath.Join(brain, "projects/acme/b.md"), "---\nname: b\ndescription: d\n---\nThe old flow is in `acme-api/src/billing.go` and `acme-api/src/flow.go`.\n", "2026-09-01")
	writeAt(t, filepath.Join(brain, "projects/acme/p.md"), "---\nname: p\ndescription: d\nkind: pitfall\napplies_to: [src/payments/**]\n---\nRetries double-charge.\n", "2026-09-01")
	writeAt(t, filepath.Join(brain, "projects/other/o.md"), "---\nname: o\ndescription: d\n---\nSee `acme-api/src/flow.go`.\n", "2026-09-01")
}

var acmeFiles = []map[string]string{
	{"filename": "src/billing.go", "status": "removed"},
	{"filename": "src/flows/main.go", "status": "renamed", "previous_filename": "src/flow.go"},
	{"filename": "src/payments/retry.go", "status": "modified"},
	{"filename": "README.md", "status": "modified"},
}

func TestAPullRequestOfABrainInTheClearGetsItsCheckAtOnce(t *testing.T) {
	gh, app := newFakeGitHub(t)
	gh.files = acmeFiles
	_, links, url, desktop := appSetup(t, app)
	acmeNotes(t, desktop.brain)
	push(t, desktop)

	body := fmt.Sprintf(pullPayload, "opened")
	if code := appDeliver(t, url, "pull_request", body); code != http.StatusOK {
		t.Fatalf("an unlinked installation: status %d", code)
	}
	if len(gh.checks) != 0 {
		t.Fatalf("an installation linked to no brain got a check: %+v", gh.checks)
	}

	if err := links.Add(Link{Kind: "github", ID: "7", Name: "acme"}); err != nil {
		t.Fatal(err)
	}
	if code := appDeliver(t, url, "pull_request", body); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	run := gh.only()
	if run.Name != "wong" || run.HeadSHA != "abc123" || run.Status != "completed" || run.Conclusion != "neutral" {
		t.Fatalf("check = %+v", run)
	}
	want := "2 notes cite files this pull request removes or moves, and 1 pitfall is noted on files it changes"
	if run.Output.Title != want {
		t.Errorf("title = %q, want %q", run.Output.Title, want)
	}
	for _, s := range []string{"| `src/billing.go` | removed | 2 |  |", "| `src/flow.go` → `src/flows/main.go` | moved | 1 |  |", "| `src/payments/retry.go` | changed |  | 1 |"} {
		if !strings.Contains(run.Output.Summary, s) {
			t.Errorf("summary lacks %q:\n%s", s, run.Output.Summary)
		}
	}
	if strings.Contains(run.Output.Summary, "README") || strings.Contains(run.Output.Summary, "a.md") {
		t.Errorf("the summary names what it should not:\n%s", run.Output.Summary)
	}
}

func TestASealedBrainsMachineAnswersThePullRequestsCheck(t *testing.T) {
	ctx := context.Background()
	gh, app := newFakeGitHub(t)
	gh.files = acmeFiles
	store, links, url, desktop := appSetup(t, app)
	desktop.client.Device = newDevice(t)
	key, _ := seal.NewKey()
	recovery, _ := seal.RecoveryDevice(seal.NewRecovery())
	if err := desktop.client.StartSeal(ctx, desktop.client.Device, recovery, key, 0); err != nil {
		t.Fatal(err)
	}
	acmeNotes(t, desktop.brain)
	push(t, desktop)
	if err := links.Add(Link{Kind: "github", ID: "7", Name: "acme"}); err != nil {
		t.Fatal(err)
	}

	if code := appDeliver(t, url, "pull_request", fmt.Sprintf(pullPayload, "synchronize")); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if run := gh.only(); run.Status != "queued" || run.Output.Title != "Waiting for one of your machines" {
		t.Fatalf("the waiting check = %+v", run)
	}
	if got, err := OpenTasks(ctx, desktop.client, desktop.brain, "acme"); err != nil || len(got) != 0 {
		t.Fatalf("a pull request left tasks: %+v %v", got, err)
	}
	run := gh.only()
	if run.Status != "completed" || run.Conclusion != "neutral" || !strings.HasPrefix(run.Output.Title, "2 notes cite") {
		t.Fatalf("the answered check = %+v", run)
	}
	if err := answerCheck(ctx, app, store, CheckAnswer{Check: 1, Linked: true}); err != nil {
		t.Fatal(err)
	}
	if again := gh.only(); again.Conclusion != "neutral" {
		t.Errorf("a second answer changed the check: %+v", again)
	}
}

func TestAPushThroughTheAppLeavesTasksAndADeletedInstallationIsForgotten(t *testing.T) {
	_, app := newFakeGitHub(t)
	_, links, url, desktop := appSetup(t, app)
	acmeNotes(t, desktop.brain)
	push(t, desktop)
	if err := links.Add(Link{Kind: "github", ID: "7", Name: "acme"}); err != nil {
		t.Fatal(err)
	}
	body := `{"ref": "refs/heads/main", "installation": {"id": 7}, "repository": {"full_name": "acme/acme-api", "default_branch": "main"},
  "commits": [{"id": "a1b2c3d4e5", "timestamp": "2026-10-13T12:00:00Z", "added": [], "removed": ["src/billing.go"]}]}`
	if code := appDeliver(t, url, "push", body); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := tasks(t, desktop, "acme"); len(got) != 2 {
		t.Fatalf("tasks = %+v", got)
	}
	if code := deliver(t, url+"/hooks/github-app", map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + sign("other", body)}, body); code != http.StatusUnauthorized {
		t.Errorf("a push signed with another secret: status %d", code)
	}
	if code := appDeliver(t, url, "installation", `{"action": "deleted", "installation": {"id": 7}}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := links.Accounts("github", "7"); len(got) != 0 {
		t.Errorf("a deleted installation is still linked to %v", got)
	}
}

func TestUserInstallationsSaysWhoCameBack(t *testing.T) {
	gh, app := newFakeGitHub(t)
	mine := Installation{ID: 7}
	mine.Account.Login, mine.Account.Type = "acme", "Organization"
	gh.users["ghu_code1"] = []Installation{mine}
	got, err := app.UserInstallations(context.Background(), "code1", "http://wong.test/webhooks/github/callback")
	if err != nil || len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("installations = %+v %v", got, err)
	}
	if u := got[0].ManageURL(""); u != "https://github.com/organizations/acme/settings/installations/7" {
		t.Errorf("manage URL = %s", u)
	}
}

func TestLinksPersistAndGoWithTheirAccount(t *testing.T) {
	file := filepath.Join(t.TempDir(), "links.json")
	links, err := OpenLinks(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []Link{{Kind: "github", ID: "7", Account: "ana"}, {Kind: "github", ID: "7", Account: "bo"}, {Kind: "linear", ID: "org", Account: "ana"}} {
		if err := links.Add(l); err != nil {
			t.Fatal(err)
		}
	}
	if err := links.DropAccount("ana"); err != nil {
		t.Fatal(err)
	}
	again, err := OpenLinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Accounts("github", "7"); len(got) != 1 || got[0] != "bo" {
		t.Errorf("accounts of the installation = %v", got)
	}
	if got := again.Of("linear", "ana"); len(got) != 0 {
		t.Errorf("a dropped account kept %+v", got)
	}
}

func TestReportCountsEachNoteOnce(t *testing.T) {
	notes := []check.Note{
		{Path: "projects/acme/a.md", Data: []byte("---\nname: a\ndescription: d\n---\n`acme-api/x.go` and `acme-api/y.go`\n"), Written: time.Now().Add(-time.Hour)},
	}
	got := prReport(notes, "acme-api", []prFile{{Path: "x.go", Status: "removed"}, {Path: "y.go", Status: "removed"}})
	if got.Notes != 1 || len(got.Files) != 2 {
		t.Errorf("report = %+v", got)
	}
}

func TestARepositoryInNoProjectHasItsCheckSkipped(t *testing.T) {
	if run := checkResult(CheckAnswer{}); run.Status != "completed" || run.Conclusion != "skipped" || !strings.Contains(run.Output.Title, "no project") {
		t.Errorf("check = %+v", run)
	}
}
