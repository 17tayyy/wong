package dashboard

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/remote"
)

func TestAnAccountsNoticeExportAndDeletion(t *testing.T) {
	dir := t.TempDir()
	folder, err := remote.OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { folder.Close() })
	store, _, err := folder.Create("acme")
	if err != nil {
		t.Fatal(err)
	}
	note := []byte("---\nname: ship-day\ndescription: When releases go out\nkind: decision\nscope: global\n---\nTuesdays.\n")
	if _, _, err := store.Apply("desktop", []remote.Upload{{Path: "me/ship-day.md", Sha: remote.Sha(note), Content: note}}); err != nil {
		t.Fatal(err)
	}
	token, err := store.NewToken("browser")
	if err != nil {
		t.Fatal(err)
	}
	refuse := true
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && refuse:
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "Cancel your subscription first, from Manage payment."})
		case r.Method == http.MethodDelete:
			json.NewEncoder(w).Encode(map[string]string{"redirect": "https://example.com/bye"})
		default:
			json.NewEncoder(w).Encode(map[string]any{"tone": "ok", "plan": map[string]string{"name": "Trial"}})
		}
	}))
	t.Cleanup(service.Close)
	h, err := Handler(folder, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Devices: remote.NewDevices(), SignIns: remote.NewSignIns(),
		Account: &AccountService{URL: service.URL + "/api/account/{account}/", Token: "service-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, srv, &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Host != strings.TrimPrefix(srv.URL, "http://") {
			return http.ErrUseLastResponse
		}
		return nil
	}}}
	if code, _ := b.post("/login", url.Values{"token": {token}}, srv.URL); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}

	if err := store.SetStanding(remote.Standing{Mode: remote.ReadOnly, Notice: "Your trial ended on 12 Nov: the brain is read-only until 12 Dec.", Link: "/account"}); err != nil {
		t.Fatal(err)
	}
	if _, body := b.get("/"); !strings.Contains(body, "Your trial ended on 12 Nov") || !strings.Contains(body, "See your plan") {
		t.Error("the library does not show the account's notice")
	}

	resp, err := b.client.Get(srv.URL + "/export")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) != 1 || z.File[0].Name != "me/ship-day.md" {
		t.Fatalf("the zip does not hold the brain: %v", err)
	}

	if code, body := b.post("/account/delete", url.Values{"confirm": {"nope"}}, srv.URL); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Type the account name, acme") {
		t.Errorf("a wrong confirmation: %d", code)
	}
	if code, body := b.post("/account/delete", url.Values{"confirm": {"acme"}}, srv.URL); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Cancel your subscription first") {
		t.Errorf("the service's refusal did not show: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "acme.db")); err != nil {
		t.Fatalf("a refused deletion deleted the brain: %v", err)
	}
	refuse = false
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/account/delete", strings.NewReader(url.Values{"confirm": {"acme"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	resp, err = b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "https://example.com/bye" {
		t.Errorf("deleting the account sent the browser to %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, err := os.Stat(filepath.Join(dir, "acme.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the brain is still on the server: %v", err)
	}
}

func TestASuspendedAccountSaysWhy(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	b := encryptionBrowser(t, store, nil)
	if err := store.SetStanding(remote.Standing{Mode: remote.Suspended, Notice: "Suspended for sending spam. Write to support@example.com."}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/history", "/encryption"} {
		if code, body := b.get(path); code != http.StatusForbidden || !strings.Contains(body, "This account is suspended") || !strings.Contains(body, "spam") {
			t.Errorf("%s on a suspended account: %d", path, code)
		}
	}
}
