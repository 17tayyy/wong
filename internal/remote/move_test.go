package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/seal"
)

// server starts an empty wong server and gives a client for it on the same device as like.
func server(t *testing.T, like *Client) (*Store, *Client) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewServer(Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Webhooks{}))
	t.Cleanup(srv.Close)
	token, err := store.NewToken("desktop")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(srv.URL, token, 5*time.Second)
	c.Device = like.Device
	return store, c
}

func versions(t *testing.T, store *Store) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestASealedBrainMovesWithItsHistoryAndItsKey(t *testing.T) {
	ctx := context.Background()
	old, _, desktop, _, _, key, code := sealed(t)
	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Ship on Tuesdays.\n")
	write(t, filepath.Join(desktop.brain, "me/use-uv.md"), "uv, never pip.\n")
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Ship on Wednesdays.\n")
	removeFile(t, filepath.Join(desktop.brain, "me/use-uv.md"))
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}

	next, to := server(t, desktop.client)
	was, err := desktop.client.Keys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := to.StartSealAs(ctx, to.Device, key, was.RecoveryKey, was.Recovery); err != nil {
		t.Fatal(err)
	}
	moved, err := CopyHistory(ctx, desktop.client, to, nil, nil)
	if err != nil || moved != versions(t, old) || versions(t, next) != moved {
		t.Fatalf("the history did not move whole: %d of %d, now %d: %v", moved, versions(t, old), versions(t, next), err)
	}
	if err := desktop.client.SetMoved(ctx, to.URL); err != nil {
		t.Fatal(err)
	}

	to.Key = key
	res, err := Pull(ctx, to, desktop.brain)
	if err != nil || len(res.Updated)+len(res.Deleted)+len(res.Conflicts) != 0 {
		t.Fatalf("the desktop is not in step with the brain on its new server: %+v %v", res, err)
	}
	write(t, filepath.Join(desktop.brain, "me/ship-day.md"), "Tuesdays.\n")
	if res, err := Push(ctx, to, desktop.brain); err != nil || len(res.Pushed) != 1 {
		t.Fatalf("the desktop cannot push to the new server: %+v %v", res, err)
	}

	keys, err := to.Keys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recovery, _ := seal.RecoveryDevice(code)
	if k, err := recovery.Unwrap(keys.Recovery); err != nil || k.ID("x") != key.ID("x") {
		t.Errorf("the recovery code does not open the brain on its new server: %v", err)
	}
	if st, err := desktop.client.Standing(ctx); err != nil || st.Moved != to.URL {
		t.Errorf("the old server does not tell where the brain went: %+v %v", st, err)
	}
	stale := NewClient(desktop.client.URL, desktop.client.Token, 5*time.Second)
	stale.Device, stale.Key = desktop.client.Device, key
	write(t, filepath.Join(desktop.brain, "me/late.md"), "written on a machine that did not follow\n")
	if _, err := Push(ctx, stale, desktop.brain); err == nil || !strings.Contains(err.Error(), "moved to "+to.URL) {
		t.Errorf("the old server took a change after the brain moved: %v", err)
	}
	if _, _, err := old.Apply("laptop", []Upload{{Path: key.ID("me/x.md"), Sha: "s", Content: []byte("x")}}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("the old server took a change after the brain moved: %v", err)
	}
}

func TestABrainInTheClearIsSealedOnItsWayToASealedServer(t *testing.T) {
	ctx := context.Background()
	_, _, desktop, _ := setup(t)
	desktop.client.Device = newDevice(t)
	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Ship on Tuesdays.\n")
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Ship on Wednesdays.\n")
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}

	next, to := server(t, desktop.client)
	next.SetLimits(Limits{Sealed: true})
	key := mustKey(t)
	recovery, _ := seal.RecoveryDevice(seal.NewRecovery())
	if err := to.ImportHistory(ctx, []Entry{{Path: "me/plain.md", Sha: "s", Content: []byte("x"), Machine: "desktop", Created: utcNow()}}, false); err == nil || !strings.Contains(err.Error(), "seal") {
		t.Errorf("a sealed server took history in the clear: %v", err)
	}
	if err := to.StartSeal(ctx, to.Device, recovery, key, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyHistory(ctx, desktop.client, to, key, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := next.db.Query(`SELECT path, content FROM history`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var path string
		var content []byte
		if err := rows.Scan(&path, &content); err != nil {
			t.Fatal(err)
		}
		if !sealedID(path) || bytes.Contains(content, []byte("Ship")) {
			t.Errorf("the new server got a file in the clear: %s", path)
		}
	}
	rows.Close()
	if versions(t, next) != 2 {
		t.Errorf("the new server holds %d versions, not 2", versions(t, next))
	}
	to.Key = key
	if res, err := Pull(ctx, to, desktop.brain); err != nil || len(res.Updated)+len(res.Conflicts) != 0 {
		t.Errorf("the desktop is not in step with the sealed brain: %+v %v", res, err)
	}
}

func TestHistoryMovesOnlyIntoAnEmptyBrain(t *testing.T) {
	store, _, desktop, _ := setup(t)
	write(t, filepath.Join(desktop.brain, "me/x.md"), "x\n")
	if _, err := Push(context.Background(), desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Path: "me/y.md", Sha: Sha([]byte("y")), Content: []byte("y"), Machine: "laptop", Created: utcNow()}
	if err := store.ImportHistory([]Entry{entry}, false); !errors.Is(err, ErrNotMoving) {
		t.Errorf("history moved into a brain that holds files: %v", err)
	}
	empty, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { empty.Close() })
	if err := empty.ImportHistory([]Entry{entry}, true); err != nil {
		t.Fatal(err)
	}
	if err := empty.ImportHistory([]Entry{entry}, false); !errors.Is(err, ErrNotMoving) {
		t.Errorf("history moved in after the move was done: %v", err)
	}
}
