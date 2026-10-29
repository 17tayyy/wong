package remote

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionsAreNotTokens(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	token, _ := store.NewToken("laptop")
	session, err := store.NewSession("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Visit(session); s.Store == nil || s.Machine != "laptop" {
		t.Errorf("Visit of a session = %+v", s)
	}
	if s, _ := store.Visit(token); s.Store != nil {
		t.Error("a token opened a dashboard session")
	}
	if s, _ := store.Auth(session); s.Store != nil {
		t.Error("a dashboard session opened the API")
	}
	if _, err := store.db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Visit(session); s.Store != nil {
		t.Error("an expired session still opens the dashboard")
	}
	again, _ := store.NewSession("laptop")
	if err := store.EndSession(again); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Visit(again); s.Store != nil {
		t.Error("a session signed out still opens the dashboard")
	}
	var left int
	store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&left)
	if left != 0 {
		t.Errorf("%d expired or ended sessions are still kept", left)
	}
}

func TestSignInLinksWorkOnce(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	links := NewSignIns()
	now := time.Now()
	links.now = func() time.Time { return now }
	code, err := links.Issue(store, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if got, via, ok := links.Redeem(code); !ok || got != store || via != "desktop" {
		t.Errorf("Redeem = %v, %q, %v", got, via, ok)
	}
	if _, _, ok := links.Redeem(code); ok {
		t.Error("a link worked twice")
	}
	late, _ := links.Issue(store, "desktop")
	now = now.Add(signInTTL + time.Second)
	if _, _, ok := links.Redeem(late); ok {
		t.Error("an expired link worked")
	}
}

func TestAdminMakesAccountsAndSignsPeopleIn(t *testing.T) {
	folder, err := OpenFolder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	links := NewSignIns()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(AdminHandler(folder, links, "admin-secret", log))
	t.Cleanup(srv.Close)
	off := httptest.NewServer(AdminHandler(folder, links, "", log))
	t.Cleanup(off.Close)
	call := func(base, method, path, token string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	for name, c := range map[string]struct{ base, token string }{"a wrong token": {srv.URL, "guess"}, "no token": {srv.URL, ""}, "a server without one": {off.URL, ""}} {
		if code, _ := call(c.base, http.MethodPut, "/admin/accounts/alice", c.token); code != http.StatusNotFound {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := call(srv.URL, http.MethodPost, "/admin/accounts/alice/signin", "admin-secret"); code != http.StatusNotFound {
		t.Errorf("sign in to an account that does not exist: %d", code)
	}
	if code, body := call(srv.URL, http.MethodPut, "/admin/accounts/alice", "admin-secret"); code != http.StatusCreated || !strings.Contains(body, `"created":true`) {
		t.Errorf("create: %d %s", code, body)
	}
	if code, _ := call(srv.URL, http.MethodPut, "/admin/accounts/alice", "admin-secret"); code != http.StatusOK {
		t.Errorf("create again: %d", code)
	}
	if code, _ := call(srv.URL, http.MethodPut, "/admin/accounts/Alice_1", "admin-secret"); code != http.StatusBadRequest {
		t.Errorf("an invalid account: %d", code)
	}
	if code, body := call(srv.URL, http.MethodGet, "/admin/accounts", "admin-secret"); code != http.StatusOK || !strings.Contains(body, `{"account":"alice","size":`) {
		t.Errorf("list accounts: %d %s", code, body)
	}
	if code, _ := call(srv.URL, http.MethodGet, "/admin/accounts", "guess"); code != http.StatusNotFound {
		t.Errorf("list accounts with a wrong token: %d", code)
	}
	code, body := call(srv.URL, http.MethodPost, "/admin/accounts/alice/signin", "admin-secret")
	var link SignInLink
	json.Unmarshal([]byte(body), &link)
	code2, ok := strings.CutPrefix(link.URL, srv.URL+"/signin/")
	if code != http.StatusOK || !ok {
		t.Fatalf("sign-in link: %d %s", code, body)
	}
	alice, _ := folder.Store("alice")
	if got, via, ok := links.Redeem(code2); !ok || got != alice || via != "sign-in" {
		t.Errorf("the link opens %v, %q, %v", got, via, ok)
	}
}

func TestMachinesMakeSignInLinks(t *testing.T) {
	store, _, desktop, _ := setup(t)
	links := NewSignIns()
	srv := httptest.NewServer(SignInHandler(store, links, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	desktop.client.URL = srv.URL
	link, err := desktop.client.SignIn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := strings.CutPrefix(link.URL, srv.URL+"/signin/")
	if got, via, ok := links.Redeem(code); !ok || got != store || via != "desktop" {
		t.Errorf("the link opens %v, %q, %v", got, via, ok)
	}
	stranger := NewClient(srv.URL, "", time.Second)
	if _, err := stranger.SignIn(t.Context()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a link without a token: %v", err)
	}
}
