package remote

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/seal"
)

func newDevice(t *testing.T) *seal.Device {
	t.Helper()
	d, err := seal.NewDevice()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// sealed sets up a server whose brain the desktop sealed, and returns the brain's key and recovery code with it.
func sealed(t *testing.T) (*Store, string, machine, machine, *seal.Device, *seal.Key, string) {
	t.Helper()
	store, url, desktop, laptop := setup(t)
	device := newDevice(t)
	key, err := seal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	code := seal.NewRecovery()
	recovery, err := seal.RecoveryDevice(code)
	if err != nil {
		t.Fatal(err)
	}
	if err := desktop.client.StartSeal(context.Background(), device, recovery, key, 0); err != nil {
		t.Fatal(err)
	}
	desktop.client.Device = device
	if err := desktop.client.Unlock(context.Background()); err != nil || desktop.client.Key == nil {
		t.Fatalf("the desktop could not unlock the brain it sealed: %v", err)
	}
	return store, url, desktop, laptop, device, key, code
}

func TestSealedBrainSyncsWithoutTheServerReadingIt(t *testing.T) {
	ctx := context.Background()
	store, _, desktop, laptop, _, _, _ := sealed(t)
	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Ship on Tuesdays, never Fridays.\n")
	write(t, filepath.Join(desktop.brain, "me/use-uv.md"), "uv, never pip.\n")
	if res, err := Push(ctx, desktop.client, desktop.brain); err != nil || len(res.Pushed) != 2 {
		t.Fatalf("push: %v %v", res, err)
	}

	rows, err := store.db.Query(`SELECT path, sha, content FROM history`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var path, sha string
		var content []byte
		if err := rows.Scan(&path, &sha, &content); err != nil {
			t.Fatal(err)
		}
		if !sealedID(path) || bytes.Contains(content, []byte("Tuesdays")) || bytes.Contains(content, []byte("acme")) || sha == Sha([]byte("uv, never pip.\n")) {
			t.Errorf("the server can read a file: %s", path)
		}
	}
	rows.Close()

	device := newDevice(t)
	laptop.client.Device = device
	if err := laptop.client.Unlock(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("a machine without a key unlocked the brain: %v", err)
	}
	if _, err := Pull(ctx, laptop.client, laptop.brain); !errors.Is(err, ErrLocked) {
		t.Fatalf("a machine without the key pulled a sealed brain: %v", err)
	}
	if err := laptop.client.RequestKey(ctx, device); err != nil {
		t.Fatal(err)
	}
	keys, err := desktop.client.Keys(ctx)
	if err != nil || len(keys.Pending) != 1 || keys.Pending[0].Device != "laptop" {
		t.Fatalf("the desktop does not see the laptop waiting: %+v %v", keys, err)
	}
	if seal.Fingerprint(keys.Pending[0].Public) != seal.Fingerprint(device.Public()) {
		t.Fatal("the fingerprint the desktop shows is not the laptop's")
	}
	if err := desktop.client.ApproveKey(ctx, keys.Pending[0], desktop.client.Key); err != nil {
		t.Fatal(err)
	}
	if err := laptop.client.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err := Pull(ctx, laptop.client, laptop.brain); err != nil || len(res.Updated) != 2 {
		t.Fatalf("pull: %v %v", res, err)
	}
	if got := read(t, filepath.Join(laptop.brain, "projects/acme/deploy-runbook.md")); got != "Ship on Tuesdays, never Fridays.\n" {
		t.Errorf("the laptop got %q", got)
	}

	write(t, filepath.Join(laptop.brain, "projects/acme/deploy-runbook.md"), "Ship on Tuesdays.\n")
	removeFile(t, filepath.Join(laptop.brain, "me/use-uv.md"))
	if _, err := Push(ctx, laptop.client, laptop.brain); err != nil {
		t.Fatal(err)
	}
	if res, err := Pull(ctx, desktop.client, desktop.brain); err != nil || len(res.Updated) != 1 || len(res.Deleted) != 1 {
		t.Fatalf("the desktop did not get the edit and the deletion: %+v %v", res, err)
	}
	if exists(filepath.Join(desktop.brain, "me/use-uv.md")) {
		t.Error("the deleted note is still on the desktop")
	}

	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Desktop's version.\n")
	write(t, filepath.Join(laptop.brain, "projects/acme/deploy-runbook.md"), "Laptop's version.\n")
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	res, err := Push(ctx, laptop.client, laptop.brain)
	if err != nil || len(res.Conflicts) != 1 || res.Conflicts[0] != "projects/acme/deploy-runbook.md" {
		t.Fatalf("the laptop's push should conflict on the runbook: %+v %v", res, err)
	}
	if got := read(t, filepath.Join(laptop.brain, conflictsDir, "projects/acme/deploy-runbook.md")); got != "Desktop's version.\n" {
		t.Errorf("the conflict copy holds %q, not the desktop's version in the clear", got)
	}
}

func TestRecoveryCodeLetsANewMachineIn(t *testing.T) {
	ctx := context.Background()
	store, url, desktop, _, _, _, code := sealed(t)
	write(t, filepath.Join(desktop.brain, "me/x.md"), "kept\n")
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	token, err := store.NewToken("phone")
	if err != nil {
		t.Fatal(err)
	}
	phone := machine{brain: t.TempDir(), client: NewClient(url, token, desktop.client.HTTP.Timeout)}
	keys, err := phone.client.Keys(ctx)
	if err != nil || !keys.Sealed || keys.Recovery == nil || keys.Wrapped != nil {
		t.Fatalf("the phone should see a sealed brain with a recovery key and none of its own: %+v %v", keys, err)
	}
	recovery, err := seal.RecoveryDevice(strings.ToLower(code))
	if err != nil {
		t.Fatal(err)
	}
	key, err := recovery.Unwrap(keys.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	device := newDevice(t)
	if err := phone.client.KeepOwnKey(ctx, device, key); err != nil {
		t.Fatal(err)
	}
	phone.client.Device = device
	if err := phone.client.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(ctx, phone.client, phone.brain); err != nil || read(t, filepath.Join(phone.brain, "me/x.md")) != "kept\n" {
		t.Fatalf("the phone did not get the note: %v", err)
	}
	if found, err := store.RevokeToken("phone"); err != nil || !found {
		t.Fatal(err)
	}
	if k, _ := store.Keys("phone"); k.Wrapped != nil {
		t.Error("a revoked machine kept its place in the keyring")
	}
}

func TestSealedBrainRefusesWhatDoesNotEncrypt(t *testing.T) {
	ctx := context.Background()
	store, url, desktop, _, device, key, _ := sealed(t)
	req, _ := http.NewRequest(http.MethodGet, url+"/api/changes", nil)
	req.Header.Set("Authorization", "Bearer "+mustToken(t, store, "old"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("a client that does not encrypt got %d from a sealed brain", resp.StatusCode)
	}
	if _, _, err := desktop.client.Push(ctx, []Upload{{Path: "me/plain.md", Sha: Sha([]byte("x")), Content: []byte("x")}}); err == nil {
		t.Error("a file in the clear was pushed into a sealed brain")
	}
	if err := store.StartSeal("desktop", device.Public(), []byte("w"), device.Public(), []byte("w"), 0); !errors.Is(err, ErrSealed) {
		t.Errorf("a sealed brain was sealed again: %v", err)
	}
	stranger := newDevice(t)
	if err := store.RequestKey("laptop", stranger.Public()); err != nil {
		t.Fatal(err)
	}
	wrapped, _ := seal.Wrap(key, stranger.Public())
	if err := store.ApproveKey("laptop", "laptop", stranger.Public(), wrapped); !errors.Is(err, ErrNoKey) {
		t.Errorf("a device without the key approved itself: %v", err)
	}
	if err := store.ApproveKey("desktop", "laptop", newDevice(t).Public(), wrapped); !errors.Is(err, ErrNoKey) {
		t.Errorf("an approval for a public key other than the one asked with went through: %v", err)
	}
}

func TestSealingABrainInTheClearReplacesItOnTheServer(t *testing.T) {
	ctx := context.Background()
	store, _, desktop, laptop := setup(t)
	for _, name := range []string{"same", "behind", "edited-here", "edited-twice", "deleted-there"} {
		write(t, filepath.Join(desktop.brain, "projects/acme", name+".md"), "Ship on Tuesdays.\n")
	}
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(ctx, laptop.client, laptop.brain); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO tasks (note, project, severity, ref, message, source, event, created_at)
		VALUES ('projects/acme/same.md', 'acme', 'stale', 'app/billing.py', 'removed', 'github acme/acme-api@1a2b3c4', '{"path":"app/billing.py"}', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO findings (project, machine, note, severity, ref, message, reported_at)
		VALUES ('acme', 'desktop', 'projects/acme/same.md', 'review', 'ENG-15', 'closed', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(desktop.brain, "projects/acme/behind.md"), "Ship on Wednesdays.\n")
	write(t, filepath.Join(desktop.brain, "projects/acme/edited-twice.md"), "Desktop's version.\n")
	removeFile(t, filepath.Join(desktop.brain, "projects/acme/deleted-there.md"))
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(laptop.brain, "projects/acme/edited-here.md"), "Laptop's version.\n")
	write(t, filepath.Join(laptop.brain, "projects/acme/edited-twice.md"), "Laptop's version.\n")

	device, key := newDevice(t), mustKey(t)
	recovery, err := seal.RecoveryDevice(seal.NewRecovery())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartSeal("desktop", device.Public(), []byte("w"), recovery.Public(), []byte("w"), 1); !errors.Is(err, ErrBehind) {
		t.Fatalf("a machine that did not pull the latest changes sealed the brain: %v", err)
	}
	if _, err := Pull(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(desktop.brain)
	if err != nil {
		t.Fatal(err)
	}
	if err := desktop.client.StartSeal(ctx, device, recovery, key, st.Cursor); err != nil {
		t.Fatal(err)
	}
	removeFile(t, filepath.Join(desktop.brain, statePath))
	desktop.client.Device, desktop.client.Key = device, key
	if res, err := Push(ctx, desktop.client, desktop.brain); err != nil || len(res.Pushed) != 4 {
		t.Fatalf("the desktop did not push the brain again encrypted: %+v %v", res, err)
	}
	var clear int
	for _, q := range []string{
		`SELECT COUNT(*) FROM history WHERE content LIKE '%Ship%' OR path LIKE '%acme%'`,
		`SELECT COUNT(*) FROM files WHERE path LIKE '%acme%'`,
		`SELECT COUNT(*) FROM findings`,
		`SELECT COUNT(*) FROM tasks`,
	} {
		if err := store.db.QueryRow(q).Scan(&clear); err != nil || clear != 0 {
			t.Errorf("the server kept something in the clear (%s): %d %v", q, clear, err)
		}
	}
	events, _, err := store.Events(0)
	if err != nil || len(events) != 1 || events[0].Remote != "acme/acme-api" {
		t.Errorf("the open task's event was not kept for the machines: %+v %v", events, err)
	}

	laptopDevice := newDevice(t)
	laptop.client = NewClient(laptop.client.URL, laptop.client.Token, laptop.client.HTTP.Timeout)
	laptop.client.Device = laptopDevice
	if _, err := Pull(ctx, laptop.client, laptop.brain); !errors.Is(err, ErrLocked) {
		t.Fatalf("the laptop synced a brain sealed after it last did, without the key: %v", err)
	}
	keys, err := desktop.client.Keys(ctx)
	if err != nil || len(keys.Pending) != 1 {
		t.Fatalf("the laptop does not wait for the key: %+v %v", keys, err)
	}
	if err := desktop.client.ApproveKey(ctx, keys.Pending[0], key); err != nil {
		t.Fatal(err)
	}
	res, err := Pull(ctx, laptop.client, laptop.brain)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Updated, []string{"projects/acme/behind.md"}) || !slices.Equal(res.Conflicts, []string{"projects/acme/edited-twice.md"}) {
		t.Errorf("the laptop should take the edit it was behind on and conflict on the file both changed: %+v", res)
	}
	if res, err := Push(ctx, laptop.client, laptop.brain); err != nil || !slices.Contains(res.Pushed, "projects/acme/edited-here.md") {
		t.Errorf("the laptop's own edit did not push: %+v %v", res, err)
	}
	if !exists(filepath.Join(laptop.brain, "projects/acme/deleted-there.md")) {
		t.Error("the laptop lost a file the server no longer has")
	}
	if _, err := Pull(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(desktop.brain, "projects/acme/edited-here.md")); got != "Laptop's version.\n" {
		t.Errorf("the laptop's edit did not reach the desktop: %q", got)
	}
}

func mustKey(t *testing.T) *seal.Key {
	t.Helper()
	key, err := seal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func mustToken(t *testing.T, store *Store, name string) string {
	t.Helper()
	token, err := store.NewToken(name)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
