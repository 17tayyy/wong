package local

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/seal"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
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

const runbook = "---\nname: deploy-runbook\ndescription: How a release goes out\nkind: reference\nscope: project:acme\n---\nShip on Tuesdays.\n"

func TestTheCopyOfASealedBrainServesAndSyncsIt(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewServer(remote.Handler(store, log, remote.Webhooks{}))
	t.Cleanup(srv.Close)
	client := func(name string, key *seal.Key) *remote.Client {
		token, err := store.NewToken(name)
		if err != nil {
			t.Fatal(err)
		}
		c := remote.NewClient(srv.URL, token, 5*time.Second)
		if c.Device, err = seal.NewDevice(); err != nil {
			t.Fatal(err)
		}
		c.Key = key
		return c
	}
	key, _ := seal.NewKey()
	recovery, _ := seal.RecoveryDevice(seal.NewRecovery())
	cloud := client("desktop", key)
	if err := cloud.StartSeal(ctx, cloud.Device, recovery, key, 0); err != nil {
		t.Fatal(err)
	}
	brain := t.TempDir()
	write(t, filepath.Join(brain, "projects/acme/deploy-runbook.md"), runbook)
	write(t, filepath.Join(brain, "projects/acme/MEMORY.md"), "- [deploy-runbook](deploy-runbook.md) — How a release goes out\n")

	m, err := Start(brain, cloud, "test", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if url, token, ok := Find(brain); !ok || url != m.URL || token != m.Token {
		t.Fatalf("another process would not find the copy: %s %v", url, ok)
	}

	link, err := remote.NewClient(m.URL, m.Token, 5*time.Second).SignIn(ctx)
	if err != nil || !strings.HasPrefix(link.URL, m.URL+"/signin/") {
		t.Fatalf("no sign-in link to the copy's dashboard: %+v %v", link, err)
	}
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	resp, err := browser.Get(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "deploy-runbook") || strings.Contains(string(page), "Your notes are encrypted") {
		t.Errorf("the copy's dashboard does not show the notes")
	}

	text := callTool(t, m, "search_notes", `{"query":"Tuesdays"}`)
	if !strings.Contains(text, "projects/acme/deploy-runbook.md") {
		t.Errorf("the copy's MCP does not find the note:\n%s", text)
	}

	edited := strings.Replace(runbook, "Ship on Tuesdays.", "Ship on Tuesdays, after the freeze.", 1)
	if _, _, err := m.store.Apply("you", []remote.Upload{{Path: "projects/acme/deploy-runbook.md", Base: remote.Sha([]byte(runbook)), Sha: remote.Sha([]byte(edited)), Content: []byte(edited)}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(brain, "projects/acme/deploy-runbook.md")); got != edited {
		t.Errorf("an edit in the dashboard did not reach the folder: %q", got)
	}
	m.last = time.Time{}
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	laptop := client("laptop", key)
	laptopBrain := t.TempDir()
	if _, err := remote.Pull(ctx, laptop, laptopBrain); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(laptopBrain, "projects/acme/deploy-runbook.md")); got != edited {
		t.Errorf("the edit did not reach another machine through the server: %q", got)
	}

	write(t, filepath.Join(brain, "me/use-uv.md"), "---\nname: use-uv\ndescription: uv, never pip\nkind: preference\nscope: global\n---\nUse uv.\n")
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if text := callTool(t, m, "read_note", `{"note":"use-uv"}`); !strings.Contains(text, "Use uv.") {
		t.Errorf("a note written in the folder did not reach the copy:\n%s", text)
	}

	if out, err := exec.Command("git", "init", "-q", brain).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", brain, "status", "--porcelain", "--untracked-files=all").Output(); err != nil || strings.Contains(string(out), "local") {
		t.Errorf("the brain's git would commit the copy or its token:\n%s %v", out, err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Find(brain); ok {
		t.Error("a closed copy is still found")
	}
}

func callTool(t *testing.T, m *Mirror, name, args string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	req, _ := http.NewRequest(http.MethodPost, m.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Result.Content) == 0 {
		t.Fatalf("%s: %v", name, err)
	}
	return out.Result.Content[0].Text
}

func TestTheCopyShowsWhatTheMachineFindsOutdated(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewServer(remote.Handler(store, log, remote.Webhooks{GitHubSecret: "s"}))
	t.Cleanup(srv.Close)
	token, _ := store.NewToken("desktop")
	cloud := remote.NewClient(srv.URL, token, 5*time.Second)
	cloud.Device, _ = seal.NewDevice()
	key, _ := seal.NewKey()
	recovery, _ := seal.RecoveryDevice(seal.NewRecovery())
	if err := cloud.StartSeal(ctx, cloud.Device, recovery, key, 0); err != nil {
		t.Fatal(err)
	}
	brain := t.TempDir()
	write(t, filepath.Join(brain, "remotes.yaml"), "acme/acme-api:\n  project: acme\n  dir: acme-api\n")
	note := filepath.Join(brain, "projects/acme/billing.md")
	write(t, note, "---\nname: billing\ndescription: Where billing lives\nkind: reference\nscope: project:acme\n---\nIn `acme-api/src/billing.go`.\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(note, old, old)
	if _, err := remote.Push(ctx, cloud, brain); err != nil {
		t.Fatal(err)
	}
	body := `{"ref": "refs/heads/main", "after": "abc1234", "repository": {"full_name": "acme/acme-api", "default_branch": "main"},
  "commits": [{"id": "abc1234", "timestamp": "` + time.Now().UTC().Format(time.RFC3339) + `", "added": [], "removed": ["src/billing.go"]}]}`
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte(body))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook: %v %v", resp, err)
	}

	m, err := Start(brain, cloud, "test", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := m.store.Tasks("acme"); err != nil || len(got) != 1 || got[0].Ref != "acme-api/src/billing.go" {
		t.Fatalf("the copy's tasks = %+v %v", got, err)
	}

	write(t, note, "---\nname: billing\ndescription: Where billing lives\nkind: reference\nscope: project:acme\n---\nIn `acme-api/src/invoices.go`.\n")
	m.last = time.Time{}
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.store.Tasks("acme"); len(got) != 0 {
		t.Errorf("the fixed note's task is still open in the copy: %+v", got)
	}
	if fixed, _ := m.store.ClosedTasks(time.Now().Add(-time.Hour)); len(fixed) != 1 {
		t.Errorf("the copy does not count the fix: %+v", fixed)
	}
}
