package remote

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type machine struct {
	brain  string
	client *Client
}

func setup(t *testing.T) (*Store, string, machine, machine) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewServer(Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Webhooks{}))
	t.Cleanup(srv.Close)
	newMachine := func(name string) machine {
		token, err := store.NewToken(name)
		if err != nil {
			t.Fatal(err)
		}
		return machine{brain: t.TempDir(), client: NewClient(srv.URL, token, 5*time.Second)}
	}
	return store, srv.URL, newMachine("desktop"), newMachine("laptop")
}

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

func pull(t *testing.T, m machine) Result {
	t.Helper()
	res, err := Pull(context.Background(), m.client, m.brain)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	return res
}

func push(t *testing.T, m machine) Result {
	t.Helper()
	res, err := Push(context.Background(), m.client, m.brain)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	return res
}

func TestSyncBetweenTwoMachines(t *testing.T) {
	_, _, desktop, laptop := setup(t)
	written := time.Date(2026, 7, 29, 22, 3, 47, 0, time.UTC)
	write(t, filepath.Join(desktop.brain, "me/use-uv.md"), "uv\n")
	write(t, filepath.Join(desktop.brain, "projects/acme/a.md"), "a\n")
	write(t, filepath.Join(desktop.brain, ".git/HEAD"), "local only\n")
	write(t, filepath.Join(desktop.brain, ".wong/imported.json"), "{}\n")
	if err := os.Chtimes(filepath.Join(desktop.brain, "projects/acme/a.md"), written, written); err != nil {
		t.Fatal(err)
	}

	if got := push(t, desktop).Pushed; !slices.Equal(got, []string{"me/use-uv.md", "projects/acme/a.md"}) {
		t.Errorf("pushed = %v", got)
	}
	if got := push(t, desktop).Pushed; len(got) != 0 {
		t.Errorf("a second push sent %v", got)
	}

	if got := pull(t, laptop).Updated; len(got) != 2 {
		t.Errorf("laptop updated = %v", got)
	}
	if got := read(t, filepath.Join(laptop.brain, "projects/acme/a.md")); got != "a\n" {
		t.Errorf("laptop a.md = %q", got)
	}
	if info, _ := os.Stat(filepath.Join(laptop.brain, "projects/acme/a.md")); !info.ModTime().Equal(written) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), written)
	}
	if _, err := os.Stat(filepath.Join(laptop.brain, ".git/HEAD")); !os.IsNotExist(err) {
		t.Error(".git was synced")
	}

	write(t, filepath.Join(laptop.brain, "projects/acme/a.md"), "a, edited on the laptop\n")
	write(t, filepath.Join(laptop.brain, "projects/acme/b.md"), "b\n")
	if err := os.Remove(filepath.Join(laptop.brain, "me/use-uv.md")); err != nil {
		t.Fatal(err)
	}
	if got := push(t, laptop).Pushed; !slices.Equal(got, []string{"me/use-uv.md", "projects/acme/a.md", "projects/acme/b.md"}) {
		t.Errorf("laptop pushed = %v", got)
	}

	res := pull(t, desktop)
	if !slices.Equal(res.Updated, []string{"projects/acme/a.md", "projects/acme/b.md"}) || !slices.Equal(res.Deleted, []string{"me/use-uv.md"}) {
		t.Errorf("desktop pull = %+v", res)
	}
	if got := read(t, filepath.Join(desktop.brain, "projects/acme/a.md")); got != "a, edited on the laptop\n" {
		t.Errorf("desktop a.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(desktop.brain, "me/use-uv.md")); !os.IsNotExist(err) {
		t.Error("the deletion did not reach the desktop")
	}
	if _, err := os.Stat(filepath.Join(desktop.brain, "me")); !os.IsNotExist(err) {
		t.Error("the deletion left an empty folder")
	}
	if _, err := os.Stat(desktop.brain); err != nil {
		t.Errorf("the brain itself went: %v", err)
	}
}

func TestPullAfterOwnPushKeepsLocalEdits(t *testing.T) {
	_, _, desktop, _ := setup(t)
	note := filepath.Join(desktop.brain, "me/a.md")
	write(t, note, "a\n")
	push(t, desktop)
	write(t, note, "edited after the push\n")

	if res := pull(t, desktop); len(res.Conflicts) != 0 || len(res.Updated) != 0 {
		t.Errorf("pull after its own push = %+v", res)
	}
	if got := read(t, note); got != "edited after the push\n" {
		t.Errorf("note = %q", got)
	}
	if got := push(t, desktop).Pushed; !slices.Equal(got, []string{"me/a.md"}) {
		t.Errorf("pushed = %v", got)
	}
}

func TestConflictsAreKeptAndResolved(t *testing.T) {
	for _, keepLocal := range []bool{true, false} {
		_, _, desktop, laptop := setup(t)
		note := "projects/acme/a.md"
		write(t, filepath.Join(desktop.brain, note), "a\n")
		push(t, desktop)
		pull(t, laptop)

		write(t, filepath.Join(desktop.brain, note), "desktop version\n")
		write(t, filepath.Join(laptop.brain, note), "laptop version\n")
		push(t, desktop)

		if got := push(t, laptop).Conflicts; !slices.Equal(got, []string{note}) {
			t.Fatalf("laptop push conflicts = %v", got)
		}
		if got := read(t, filepath.Join(laptop.brain, note)); got != "laptop version\n" {
			t.Errorf("the conflict overwrote the laptop's file: %q", got)
		}
		if got := read(t, filepath.Join(laptop.brain, ".wong/conflicts", note)); got != "desktop version\n" {
			t.Errorf("server copy = %q", got)
		}
		if got := pull(t, laptop).Conflicts; !slices.Equal(got, []string{note}) {
			t.Errorf("laptop pull conflicts = %v", got)
		}
		if got := push(t, laptop).Pushed; len(got) != 0 {
			t.Errorf("a file in conflict was pushed: %v", got)
		}

		if err := Resolve(laptop.brain, note, keepLocal); err != nil {
			t.Fatal(err)
		}
		push(t, laptop)
		pull(t, desktop)
		want := "laptop version\n"
		if !keepLocal {
			want = "desktop version\n"
		}
		for _, m := range []machine{laptop, desktop} {
			if got := read(t, filepath.Join(m.brain, note)); got != want {
				t.Errorf("keepLocal=%v: %s has %q, want %q", keepLocal, m.brain, got, want)
			}
		}
		if _, err := os.Stat(filepath.Join(laptop.brain, ".wong/conflicts", note)); !os.IsNotExist(err) {
			t.Error("the conflict copy was left behind")
		}
		if err := Resolve(laptop.brain, note, true); err == nil {
			t.Error("Resolve() of a note with no conflict succeeded")
		}
	}
}

func TestTheServerRefusesBadRequests(t *testing.T) {
	_, url, desktop, _ := setup(t)

	resp, err := http.Get(url + "/api/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("health: %v, %v", resp, err)
	}
	for _, token := range []string{"", "wong_unknown"} {
		c := NewClient(url, token, time.Second)
		if _, _, err := c.Changes(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "401") {
			t.Errorf("token %q: %v", token, err)
		}
	}
	for _, bad := range []Upload{
		{Path: "../etc/passwd", Sha: Sha([]byte("x")), Content: []byte("x")},
		{Path: ".git/config", Sha: Sha([]byte("x")), Content: []byte("x")},
		{Path: "me/a.md", Sha: "not-the-sha", Content: []byte("x")},
	} {
		if _, _, err := desktop.client.Push(context.Background(), []Upload{bad}); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("push %q: %v", bad.Path, err)
		}
	}
}

func TestStoreKeepsEveryVersion(t *testing.T) {
	store, _, _, _ := setup(t)
	for i, content := range []string{"one", "two"} {
		base := ""
		if i > 0 {
			base = Sha([]byte("one"))
		}
		if _, conflicts, err := store.Apply("desktop", []Upload{{Path: "me/a.md", Base: base, Sha: Sha([]byte(content)), Content: []byte(content)}}); err != nil || len(conflicts) != 0 {
			t.Fatalf("Apply %q: %v, %v", content, conflicts, err)
		}
	}
	var versions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM history WHERE path = 'me/a.md'`).Scan(&versions); err != nil || versions != 2 {
		t.Errorf("versions = %d, %v", versions, err)
	}
	changes, cursor, err := store.Changes(0)
	if err != nil || len(changes) != 1 || !bytes.Equal(changes[0].Content, []byte("two")) || cursor != 2 {
		t.Errorf("Changes(0) = %+v, %d, %v", changes, cursor, err)
	}
	if changes, cursor, _ := store.Changes(2); len(changes) != 0 || cursor != 2 {
		t.Errorf("Changes(2) = %+v, %d", changes, cursor)
	}
}
