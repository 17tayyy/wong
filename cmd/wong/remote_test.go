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

	if _, err := run(t, "--brain", desktop, "sync"); err == nil || !strings.Contains(err.Error(), "no server set") {
		t.Errorf("sync without a server: %v", err)
	}
	if out, err := runWithInput(t, token+"\n", "remote", srv.URL); err != nil {
		t.Fatalf("remote: %v\n%s", err, out)
	}
	if info, err := os.Stat(filepath.Join(home, ".config", "wong", "remote.yaml")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("remote.yaml: %v, %v", info, err)
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
