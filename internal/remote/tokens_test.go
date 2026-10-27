package remote

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokensAreListedAndRevoked(t *testing.T) {
	store, url, desktop, laptop := setup(t)

	if _, _, err := desktop.client.Changes(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	tokens, err := store.Tokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].Name != "desktop" || tokens[0].LastUsed == "" || tokens[1].Name != "laptop" || tokens[1].LastUsed != "" {
		t.Fatalf("tokens = %+v", tokens)
	}

	if found, err := store.RevokeToken("laptop"); err != nil || !found {
		t.Fatalf("RevokeToken = %v, %v", found, err)
	}
	if _, _, err := laptop.client.Changes(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a revoked token still works: %v", err)
	}
	if _, _, err := NewClient(url, desktop.client.Token, time.Second).Changes(context.Background(), 0); err != nil {
		t.Errorf("revoking one machine locked out another: %v", err)
	}
	if found, err := store.RevokeToken("phone"); err != nil || found {
		t.Errorf("RevokeToken of an unknown name = %v, %v", found, err)
	}
}

func TestOpenAddsTheColumnsOfNewerVersions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "wong.db")
	db, err := sql.Open("sqlite", "file:"+file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE tokens (name TEXT PRIMARY KEY, hash TEXT NOT NULL, created_at TEXT NOT NULL);
		INSERT INTO tokens VALUES ('desktop', '` + Sha([]byte("wong_old")) + `', '2026-10-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if name, err := store.Machine("wong_old"); err != nil || name != "desktop" {
		t.Errorf("Machine = %q, %v", name, err)
	}
	if tokens, err := store.Tokens(); err != nil || len(tokens) != 1 || tokens[0].LastUsed == "" {
		t.Errorf("tokens = %+v, %v", tokens, err)
	}
}

func TestCheckURL(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://wong.example.com":     true,
		"http://192.168.0.10:8787":     true,
		"http://10.0.0.2:8787":         true,
		"http://100.64.0.10:8787":      true,
		"http://localhost:8787":        true,
		"http://[::1]:8787":            true,
		"http://homelab:8787":          true,
		"http://wong.local:8787":       true,
		"http://wong.tail1234.ts.net/": true,
		"http://wong.example.com":      false,
		"http://203.0.113.7:8787":      false,
		"ftp://wong.example.com":       false,
		"wong.example.com":             false,
	} {
		if err := CheckURL(url); (err == nil) != ok {
			t.Errorf("CheckURL(%q) = %v", url, err)
		}
	}
}
