package remote

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAReadOnlyAccountReadsButDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	store, _, desktop, laptop := setup(t)
	write(t, filepath.Join(desktop.brain, "me/use-uv.md"), "uv, never pip.\n")
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Fatal(err)
	}
	notice := "Your trial ended on 12 Nov. The brain is read-only until 12 Dec, then deleted."
	if err := store.SetStanding(Standing{Mode: ReadOnly, Notice: notice, Link: "https://example.com/account"}); err != nil {
		t.Fatal(err)
	}
	if res, err := Pull(ctx, laptop.client, laptop.brain); err != nil || len(res.Updated) != 1 {
		t.Fatalf("a read-only account could not be pulled: %+v %v", res, err)
	}
	write(t, filepath.Join(desktop.brain, "me/use-uv.md"), "uv, always.\n")
	_, err := Push(ctx, desktop.client, desktop.brain)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), notice) || !strings.Contains(err.Error(), "example.com/account") {
		t.Errorf("a push to a read-only account: %v", err)
	}
	note := []byte("---\nname: ship-day\ndescription: When releases go out\nkind: decision\nscope: global\n---\nTuesdays.\n")
	if _, err := store.ApplyOp("you", NewOp("create"), []Upload{{Path: "me/ship-day.md", Sha: Sha(note), Content: note}}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("the dashboard or MCP wrote to a read-only account: %v", err)
	}
	st, err := desktop.client.Standing(ctx)
	if err != nil || st.Notice != notice || st.Mode != ReadOnly {
		t.Errorf("the client does not learn the account's notice: %+v %v", st, err)
	}

	if err := store.SetStanding(Standing{Mode: Suspended, Notice: "Suspended for sending spam. Write to support@example.com."}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(ctx, laptop.client, laptop.brain); err == nil || !strings.Contains(err.Error(), "spam") {
		t.Errorf("a suspended account was pulled: %v", err)
	}
	if err := store.SetStanding(Standing{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(ctx, desktop.client, desktop.brain); err != nil {
		t.Errorf("an account back in standing could not push: %v", err)
	}
}

func TestAnAccountKeepsItsOwnSizeLimit(t *testing.T) {
	store, _, _, _ := setup(t)
	store.SetLimits(Limits{Size: 1 << 20})
	if err := store.SetStanding(Standing{Size: 100}); err != nil {
		t.Fatal(err)
	}
	big := []byte(strings.Repeat("x", 200))
	if _, _, err := store.Apply("desktop", []Upload{{Path: "me/big.md", Sha: Sha(big), Content: big}}); !errors.Is(err, ErrLimit) {
		t.Errorf("the account's own limit was not kept: %v", err)
	}
	if store.SizeLimit() != 100 {
		t.Errorf("SizeLimit = %d", store.SizeLimit())
	}
}

func TestTheAdminSetsStandingsAndDeletesAccounts(t *testing.T) {
	dir := t.TempDir()
	folder, err := OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	if _, _, err := folder.Create("acme"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(AdminHandler(folder, NewSignIns(), "admin-secret", slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	call := func(method, path, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer admin-secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := call(http.MethodPut, "/admin/accounts/acme/standing", `{"mode":"read-only","notice":"Your trial ended.","until":"2026-12-12T00:00:00Z"}`); code != http.StatusOK {
		t.Fatalf("set standing: %d", code)
	}
	if code := call(http.MethodPut, "/admin/accounts/acme/standing", `{"mode":"gone"}`); code == http.StatusOK {
		t.Error("an unknown mode was taken")
	}
	accounts, err := folder.Accounts()
	if err != nil || len(accounts) != 1 || accounts[0].Standing.Mode != ReadOnly || accounts[0].Standing.Until == "" {
		t.Fatalf("the accounts do not carry their standing: %+v %v", accounts, err)
	}
	if code := call(http.MethodDelete, "/admin/accounts/acme", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "acme.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the account's brain is still on disk: %v", err)
	}
	if code := call(http.MethodDelete, "/admin/accounts/acme", ""); code != http.StatusNotFound {
		t.Errorf("deleting it twice: %d", code)
	}
	if code := call(http.MethodPut, "/admin/accounts/acme/standing", `{}`); code != http.StatusNotFound {
		t.Errorf("a deleted account took a standing: %d", code)
	}
}
