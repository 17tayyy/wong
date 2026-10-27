package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/remote"
)

func TestRemoteSync(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := remote.Open(filepath.Join(home, "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := httptest.NewServer(remote.Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), remote.Webhooks{}))
	defer srv.Close()
	token, err := store.NewToken("desktop")
	if err != nil {
		t.Fatal(err)
	}
	desktop, laptop := filepath.Join(home, "desktop"), filepath.Join(home, "laptop")
	if err := os.MkdirAll(filepath.Join(desktop, "me"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktop, "me", "a.md"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, cmd := range []string{"sync", "tasks"} {
		if _, err := run(t, "--brain", desktop, cmd); err == nil || !strings.Contains(err.Error(), "no server set") {
			t.Errorf("%s without a server: %v", cmd, err)
		}
	}
	if _, err := runWithInput(t, token+"\n", "remote", "http://wong.example.com"); err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Errorf("remote over plain http to a public host: %v", err)
	}
	if out, err := runWithInput(t, token+"\n", "remote", srv.URL); err != nil {
		t.Fatalf("remote: %v\n%s", err, out)
	}
	if info, err := os.Stat(filepath.Join(home, ".config", "wong", "remote.yaml")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("remote.yaml: %v, %v", info, err)
	}

	if out, err := run(t, "tasks", "--project", "acme"); err != nil || out != "0 open tasks\n" {
		t.Errorf("tasks: %v\n%s", err, out)
	}
	if out, err := run(t, "--brain", desktop, "push"); err != nil || !strings.Contains(out, "pushed: 1") {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if out, err := run(t, "--brain", laptop, "pull"); err != nil || !strings.Contains(out, "pulled: 1 updated") {
		t.Fatalf("pull: %v\n%s", err, out)
	}

	for brain, content := range map[string]string{desktop: "desktop\n", laptop: "laptop\n"} {
		if err := os.WriteFile(filepath.Join(brain, "me", "a.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run(t, "--brain", desktop, "sync"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "--brain", laptop, "sync")
	if err != nil || !strings.Contains(out, "1 files changed both here and on the server") {
		t.Fatalf("sync with a conflict: %v\n%s", err, out)
	}
	if out, err := run(t, "--brain", laptop, "resolve", "me/a.md", "--keep", "server"); err != nil || !strings.Contains(out, "kept the server version") {
		t.Fatalf("resolve: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(laptop, "me", "a.md")); string(data) != "desktop\n" {
		t.Errorf("laptop a.md = %q", data)
	}
}

func TestPrintTasks(t *testing.T) {
	var out strings.Builder
	tasks := []remote.Task{
		{Note: "projects/acme/a.md", Severity: "stale", Ref: "ENG-15", Message: "Done since 2026-10-20", Source: "linear ENG-15"},
		{Note: "projects/acme/a.md", Severity: "stale", Ref: "src/app.go", Message: "deleted", Source: "github acme/api@a1b2c3d"},
		{Note: "projects/acme/b.md", Severity: "review", Ref: "ENG-16", Message: "Done on 2026-10-21", Source: "linear ENG-16"},
	}
	if err := printTasks(&out, tasks); err != nil {
		t.Fatal(err)
	}
	want := `projects/acme/a.md
  stale  ENG-15      Done since 2026-10-20 (linear ENG-15)
  stale  src/app.go  deleted (github acme/api@a1b2c3d)

projects/acme/b.md
  review  ENG-16  Done on 2026-10-21 (linear ENG-16)

3 open tasks
`
	if out.String() != want {
		t.Errorf("printTasks =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestServeTokens(t *testing.T) {
	db := filepath.Join(t.TempDir(), "wong.db")
	for _, name := range []string{"desktop", "laptop"} {
		if out, err := run(t, "serve", "--db", db, "--new-token", name); err != nil || !strings.HasPrefix(out, "wong_") {
			t.Fatalf("new token: %v\n%s", err, out)
		}
	}
	if out, err := run(t, "serve", "--db", db, "--revoke-token", "laptop"); err != nil || out != "revoked the token of laptop\n" {
		t.Errorf("revoke: %v\n%s", err, out)
	}
	if _, err := run(t, "serve", "--db", db, "--revoke-token", "laptop"); err == nil {
		t.Error("revoking a token twice did not fail")
	}
	out, err := run(t, "serve", "--db", db, "--tokens")
	if err != nil || !strings.Contains(out, "NAME") || !strings.Contains(out, "desktop") || !strings.Contains(out, "never") || strings.Contains(out, "laptop") {
		t.Errorf("tokens: %v\n%s", err, out)
	}
	if _, err := run(t, "serve", "--db", db, "--tls-cert", "cert.pem"); err == nil || !strings.Contains(err.Error(), "go together") {
		t.Errorf("--tls-cert without --tls-key: %v", err)
	}
}
