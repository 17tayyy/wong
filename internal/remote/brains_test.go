package remote

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFolderKeepsEachBrainToItsTokens(t *testing.T) {
	dir := t.TempDir()
	folder, err := OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	newToken := func(brain, name string) string {
		t.Helper()
		store, _, err := folder.Create(brain)
		if err != nil {
			t.Fatal(err)
		}
		token, err := store.NewToken(name)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	aliceLaptop, aliceDesktop, bobLaptop := newToken("alice", "laptop"), newToken("alice", "desktop"), newToken("bob", "laptop")
	if !strings.HasPrefix(aliceLaptop, "wong_alice_") {
		t.Errorf("a token of the brain alice does not name it: %s", aliceLaptop[:12])
	}
	if _, created, err := folder.Create("alice"); err != nil || created {
		t.Errorf("Create of a brain the folder has = %v, %v", created, err)
	}
	srv := httptest.NewServer(Handler(folder, slog.New(slog.NewTextHandler(io.Discard, nil)), Webhooks{}))
	t.Cleanup(srv.Close)
	client := func(token string) *Client { return NewClient(srv.URL, token, 5*time.Second) }
	ctx := context.Background()

	note := []byte("---\nname: deploy\ndescription: How a release goes out\n---\nMerge to main.\n")
	if accepted, _, err := client(aliceLaptop).Push(ctx, []Upload{{Path: "projects/acme/deploy.md", Sha: Sha(note), Content: note}}); err != nil || len(accepted) != 1 {
		t.Fatalf("push to alice = %v, %v", accepted, err)
	}
	if changes, _, err := client(aliceDesktop).Changes(ctx, 0); err != nil || len(changes) != 1 {
		t.Errorf("alice's other machine got %v, %v", changes, err)
	}
	if changes, _, err := client(bobLaptop).Changes(ctx, 0); err != nil || len(changes) != 0 {
		t.Errorf("bob's token reads alice's brain: %v, %v", changes, err)
	}

	secret := aliceLaptop[len("wong_alice_"):]
	for name, token := range map[string]string{
		"alice's secret under bob": "wong_bob_" + secret,
		"a brain the folder lacks": "wong_carol_" + secret,
		"a token without a brain":  "wong_" + secret,
		"a path for a brain":       "wong_.._" + secret,
	} {
		if _, _, err := client(token).Changes(ctx, 0); err == nil || !strings.Contains(err.Error(), "401") {
			t.Errorf("%s opened a brain: %v", name, err)
		}
	}
	if ids, err := folder.IDs(); err != nil || !slices.Equal(ids, []string{"alice", "bob"}) {
		t.Errorf("IDs = %v, %v; a token for an unknown brain must not create it", ids, err)
	}
}

func TestBrainOfAFolderServesAloneWithItsTokens(t *testing.T) {
	dir := t.TempDir()
	folder, err := OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, _, err := folder.Create("alice")
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.NewToken("laptop")
	if err != nil {
		t.Fatal(err)
	}
	folder.Close()
	if err := os.Rename(filepath.Join(dir, "alice.db"), filepath.Join(dir, "wong.db")); err != nil {
		t.Fatal(err)
	}

	alone, err := Open(filepath.Join(dir, "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer alone.Close()
	if s, err := alone.Auth(token); err != nil || s.Machine != "laptop" || s.Account != "" {
		t.Errorf("the brain served alone refused its token: %+v, %v", s, err)
	}
}

func TestValidAccount(t *testing.T) {
	for _, id := range []string{"alice", "acme-team", "a1"} {
		if err := ValidAccount(id); err != nil {
			t.Errorf("ValidAccount(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "Alice", "a_b", "../x", "-a", "a.db", strings.Repeat("a", 64)} {
		if err := ValidAccount(id); err == nil {
			t.Errorf("ValidAccount(%q) accepted it", id)
		}
	}
}
