package remote

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/17tayyy/wong/internal/seal"
)

// asCloudAI reads the files of store's brain as an AI in the cloud would, and writes ups first when given.
func asCloudAI(t *testing.T, store *Store, ups ...Upload) (map[string]string, bool) {
	t.Helper()
	files := map[string]string{}
	ok, err := store.OpenForCloudAIs("claude.ai", func(plain *Store) error {
		if len(ups) > 0 {
			if _, conflicts, err := plain.Apply("claude.ai", ups); err != nil || len(conflicts) > 0 {
				return errors.Join(err, errors.New("conflict"))
			}
		}
		changes, _, err := plain.Changes(0)
		for _, ch := range changes {
			if !ch.Deleted {
				files[ch.Path] = string(ch.Content)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, ok
}

func TestCloudAIsReadAndWriteASealedBrainOnlyWhileLetIn(t *testing.T) {
	ctx := context.Background()
	store, _, desktop, _, _, key, _ := sealed(t)
	cloud, err := NewCloudAIs(filepath.Join(t.TempDir(), "cloud-key"))
	if err != nil {
		t.Fatal(err)
	}
	store.SetCloudAIs(cloud)
	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Ship on Tuesdays.\n")
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	if _, ok := asCloudAI(t, store); ok {
		t.Fatal("an AI in the cloud read a brain its owner did not let it read")
	}

	if err := store.LetCloudAIs("laptop", mustWrap(t, key, cloud.Public())); !errors.Is(err, ErrNoKey) {
		t.Errorf("a machine without the key let the cloud AIs in: %v", err)
	}
	if err := store.LetCloudAIs("desktop", mustWrap(t, mustKey(t), newDevice(t).Public())); !errors.Is(err, ErrNoKey) {
		t.Errorf("a key the server cannot open was taken: %v", err)
	}
	if err := desktop.client.LetCloudAIs(ctx, key); err != nil {
		t.Fatal(err)
	}
	if keys, err := desktop.client.Keys(ctx); err != nil || !keys.CloudAIs {
		t.Errorf("the keyring does not say the cloud AIs are in: %+v %v", keys, err)
	}
	files, ok := asCloudAI(t, store)
	if !ok || files["projects/acme/deploy-runbook.md"] != "Ship on Tuesdays.\n" {
		t.Fatalf("the cloud AI did not read the note: %v %v", ok, files)
	}

	uv := []byte("uv, never pip.\n")
	if _, ok := asCloudAI(t, store, Upload{Path: "me/use-uv.md", Sha: Sha(uv), Content: uv}); !ok {
		t.Fatal("the cloud AI could not write")
	}
	rows, err := store.db.Query(`SELECT path, content FROM history`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var path string
		var content []byte
		if err := rows.Scan(&path, &content); err != nil {
			t.Fatal(err)
		}
		if !sealedID(path) || bytes.Contains(content, []byte("pip")) {
			t.Errorf("the cloud AI's note reached the server in the clear: %s", path)
		}
	}
	rows.Close()
	if res, err := Pull(ctx, desktop.client, desktop.brain); err != nil || len(res.Updated) != 1 || read(t, filepath.Join(desktop.brain, "me/use-uv.md")) != string(uv) {
		t.Fatalf("the cloud AI's note did not reach the desktop: %+v %v", res, err)
	}

	write(t, filepath.Join(desktop.brain, "projects/acme/deploy-runbook.md"), "Ship on Wednesdays.\n")
	removeFile(t, filepath.Join(desktop.brain, "me/use-uv.md"))
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	files, _ = asCloudAI(t, store)
	if _, still := files["me/use-uv.md"]; still || files["projects/acme/deploy-runbook.md"] != "Ship on Wednesdays.\n" {
		t.Errorf("the cloud AI does not see what the desktop changed since: %v", files)
	}

	if err := desktop.client.StopCloudAIs(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := asCloudAI(t, store); ok {
		t.Error("the cloud AIs still read the brain after they were stopped")
	}
}

func mustWrap(t *testing.T, key *seal.Key, public []byte) []byte {
	t.Helper()
	wrapped, err := seal.Wrap(key, public)
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}
