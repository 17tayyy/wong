package remote

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	githubSecret = "github-secret"
	linearSecret = "linear-secret"
)

func hookSetup(t *testing.T) (string, machine) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewServer(Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Webhooks{GitHubSecret: githubSecret, LinearSecret: linearSecret}))
	t.Cleanup(srv.Close)
	token, err := store.NewToken("desktop")
	if err != nil {
		t.Fatal(err)
	}
	return srv.URL, machine{brain: t.TempDir(), client: NewClient(srv.URL, token, 5*time.Second)}
}

func writeAt(t *testing.T, path, content, day string) {
	t.Helper()
	write(t, path, content)
	when, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func deliver(t *testing.T, url string, headers map[string]string, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func tasks(t *testing.T, m machine, project string) []Task {
	t.Helper()
	got, err := m.client.Tasks(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

const githubPayload = `{
  "ref": "refs/heads/%s",
  "repository": {"full_name": "Acme/acme-api", "default_branch": "main"},
  "commits": [
    {"id": "a1b2c3d4e5f60718293a", "timestamp": "2026-10-13T12:00:00+02:00", "added": [], "removed": ["src/billing.go", "src/old.go"]},
    {"id": "f0e1d2c3b4a596877869", "timestamp": "2026-10-13T12:05:00+02:00", "added": ["src/old.go"], "removed": []}
  ]
}`

func TestGitHubPushOpensTasksUntilTheNoteIsFixed(t *testing.T) {
	url, desktop := hookSetup(t)
	write(t, filepath.Join(desktop.brain, "remotes.yaml"), "acme/acme-api:\n  project: acme\n  dir: acme-api\n")
	writeAt(t, filepath.Join(desktop.brain, "projects/acme/a.md"), "---\nname: a\ndescription: d\n---\nBilling lives in `acme-api/src/billing.go` and `src/old.go`.\n", "2026-09-01")
	writeAt(t, filepath.Join(desktop.brain, "projects/other/b.md"), "---\nname: b\ndescription: d\n---\nBilling lives in `acme-api/src/billing.go`.\n", "2026-09-01")
	push(t, desktop)
	body := fmt.Sprintf(githubPayload, "main")
	headers := map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + sign(githubSecret, body)}

	for range 2 {
		if code := deliver(t, url+"/hooks/github", headers, body); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
	}
	got := tasks(t, desktop, "acme")
	if len(got) != 1 || got[0].Note != "projects/acme/a.md" || got[0].Ref != "acme-api/src/billing.go" || got[0].Severity != "stale" ||
		got[0].Source != "github Acme/acme-api@a1b2c3d" {
		t.Fatalf("tasks = %+v", got)
	}
	if got := tasks(t, desktop, "other"); len(got) != 0 {
		t.Errorf("a note of another project got tasks: %+v", got)
	}

	write(t, filepath.Join(desktop.brain, "projects/acme/a.md"), "---\nname: a\ndescription: d\n---\nBilling lives in `acme-api/src/invoices.go`.\n")
	push(t, desktop)
	if got := tasks(t, desktop, ""); len(got) != 0 {
		t.Errorf("tasks after the fix = %+v", got)
	}
}

func TestGitHubHookIgnoresWhatItShould(t *testing.T) {
	url, desktop := hookSetup(t)
	writeAt(t, filepath.Join(desktop.brain, "projects/acme/a.md"), "---\nname: a\ndescription: d\n---\nBilling lives in `acme-api/src/billing.go`.\n", "2026-09-01")
	push(t, desktop)
	main := fmt.Sprintf(githubPayload, "main")

	if code := deliver(t, url+"/hooks/github", map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + sign("wrong", main)}, main); code != http.StatusUnauthorized {
		t.Errorf("bad signature: status %d", code)
	}
	if code := deliver(t, url+"/hooks/github", map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + sign(githubSecret, main)}, main); code != http.StatusOK {
		t.Errorf("repo in no project: status %d", code)
	}
	write(t, filepath.Join(desktop.brain, "remotes.yaml"), "acme/acme-api:\n  project: acme\n  dir: acme-api\n")
	push(t, desktop)
	branch := fmt.Sprintf(githubPayload, "feature")
	if code := deliver(t, url+"/hooks/github", map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + sign(githubSecret, branch)}, branch); code != http.StatusOK {
		t.Errorf("other branch: status %d", code)
	}
	ping := `{"zen": "Keep it logically awesome."}`
	if code := deliver(t, url+"/hooks/github", map[string]string{"X-GitHub-Event": "ping", "X-Hub-Signature-256": "sha256=" + sign(githubSecret, ping)}, ping); code != http.StatusOK {
		t.Errorf("ping: status %d", code)
	}
	if got := tasks(t, desktop, ""); len(got) != 0 {
		t.Errorf("tasks = %+v", got)
	}
}

func linearPayload(state, kind string, sent time.Time) string {
	return fmt.Sprintf(`{"action": "update", "type": "Issue", "createdAt": "2026-10-20T10:00:00.000Z", "webhookTimestamp": %d,
  "data": {"identifier": "ENG-15", "completedAt": "2026-10-20T10:00:00.000Z", "canceledAt": null, "state": {"name": %q, "type": %q}},
  "updatedFrom": {"stateId": "s1"}}`, sent.UnixMilli(), state, kind)
}

func TestLinearIssueClosedOpensTasksUntilTheNoteIsFixed(t *testing.T) {
	url, desktop := hookSetup(t)
	writeAt(t, filepath.Join(desktop.brain, "projects/acme/d.md"), "---\nname: d\ndescription: d\n---\nENG-15 en pausa hasta la demo.\n", "2026-10-01")
	writeAt(t, filepath.Join(desktop.brain, "me/p.md"), "---\nname: p\ndescription: d\nkind: preference\n---\nLike ENG-15, en pausa.\n", "2026-10-01")
	push(t, desktop)
	send := func(body string) int {
		return deliver(t, url+"/hooks/linear", map[string]string{"Linear-Event": "Issue", "Linear-Signature": sign(linearSecret, body)}, body)
	}

	if code := send(linearPayload("In Progress", "started", time.Now())); code != http.StatusOK {
		t.Fatalf("open issue: status %d", code)
	}
	if got := tasks(t, desktop, ""); len(got) != 0 {
		t.Fatalf("an open issue opened tasks: %+v", got)
	}
	if code := send(linearPayload("Done", "completed", time.Now().Add(-2*time.Minute))); code != http.StatusUnauthorized {
		t.Errorf("old webhookTimestamp: status %d", code)
	}
	body := linearPayload("Done", "completed", time.Now())
	if code := deliver(t, url+"/hooks/linear", map[string]string{"Linear-Signature": sign("wrong", body)}, body); code != http.StatusUnauthorized {
		t.Errorf("bad signature: status %d", code)
	}
	if code := send(body); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	got := tasks(t, desktop, "acme")
	if len(got) != 1 || got[0].Note != "projects/acme/d.md" || got[0].Ref != "ENG-15" || got[0].Severity != "stale" || got[0].Source != "linear ENG-15" {
		t.Fatalf("tasks = %+v", got)
	}
	if got := tasks(t, desktop, "me"); len(got) != 0 {
		t.Errorf("a preference got tasks: %+v", got)
	}

	write(t, filepath.Join(desktop.brain, "projects/acme/d.md"), "---\nname: d\ndescription: d\n---\nENG-15 en pausa hasta la demo de enero.\n")
	push(t, desktop)
	if got := tasks(t, desktop, "acme"); len(got) != 1 {
		t.Errorf("a push that still says the issue is paused closed the task: %+v", got)
	}
	write(t, filepath.Join(desktop.brain, "projects/acme/d.md"), "---\nname: d\ndescription: d\n---\nENG-15 is done.\n")
	push(t, desktop)
	if got := tasks(t, desktop, "acme"); len(got) != 0 {
		t.Errorf("tasks after the fix = %+v", got)
	}
}

func TestWebhooksAreOffWithoutASecret(t *testing.T) {
	_, url, _, _ := setup(t)

	for _, path := range []string{"/hooks/github", "/hooks/linear"} {
		if code := deliver(t, url+path, nil, "{}"); code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, code)
		}
	}
}
