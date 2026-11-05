package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeLinear struct {
	challenge string
	refreshed []string
	revoked   []string
}

func (f *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	switch r.URL.Path {
	case "/oauth/token":
		if r.Form.Get("client_id") != "client" || r.Form.Has("client_secret") {
			http.Error(w, `{"error": "invalid_client"}`, http.StatusUnauthorized)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if Challenge(r.Form.Get("code_verifier")) != f.challenge || r.Form.Get("code") != "the-code" {
				http.Error(w, `{"error": "invalid_grant"}`, http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "lin_oauth_1", "refresh_token": "refresh_1", "expires_in": 86399})
		case "refresh_token":
			f.refreshed = append(f.refreshed, r.Form.Get("refresh_token"))
			json.NewEncoder(w).Encode(map[string]any{"access_token": "lin_oauth_2", "refresh_token": "refresh_2", "expires_in": 86399})
		}
	case "/oauth/revoke":
		f.revoked = append(f.revoked, r.Form.Get("token"))
	case "/graphql":
		if r.Header.Get("Authorization") != "Bearer lin_oauth_1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data": {"organization": {"id": "org-1", "name": "Acme"}}}`))
	default:
		http.NotFound(w, r)
	}
}

func TestConnectRenewAndForget(t *testing.T) {
	ctx := context.Background()
	f := &fakeLinear{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	o := OAuth{ClientID: "client", Web: "https://linear.test", API: srv.URL}

	verifier := NewVerifier()
	u, err := url.Parse(o.AuthorizeURL("https://wong.test/webhooks/linear/callback", "s1", verifier))
	if err != nil {
		t.Fatal(err)
	}
	if q := u.Query(); u.Host != "linear.test" || q.Get("code_challenge") != Challenge(verifier) || q.Get("scope") != "read" || q.Get("state") != "s1" {
		t.Fatalf("authorize URL = %s", u)
	}
	f.challenge = Challenge(verifier)
	if _, err := o.Exchange(ctx, "the-code", "https://wong.test/webhooks/linear/callback", "another"); err == nil {
		t.Fatal("a code was traded with the wrong verifier")
	}
	tok, err := o.Exchange(ctx, "the-code", "https://wong.test/webhooks/linear/callback", verifier)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "lin_oauth_1" || tok.Organization != "org-1" || tok.Name != "Acme" || time.Until(tok.Expires) < 23*time.Hour {
		t.Fatalf("token = %+v", tok)
	}

	brain := t.TempDir()
	os.MkdirAll(filepath.Join(brain, ".git", "info"), 0o755)
	if err := SaveToken(brain, tok); err != nil {
		t.Fatal(err)
	}
	if c, ok, err := o.FromBrain(ctx, brain); err != nil || !ok || c.Key != "lin_oauth_1" || len(f.refreshed) != 0 {
		t.Fatalf("a fresh token: %+v %v %v, renewed %v", c, ok, err, f.refreshed)
	}
	tok.Expires = time.Now().Add(time.Minute)
	SaveToken(brain, tok)
	c, _, err := o.FromBrain(ctx, brain)
	if err != nil || c.Key != "lin_oauth_2" || len(f.refreshed) != 1 || f.refreshed[0] != "refresh_1" {
		t.Fatalf("an expiring token was not renewed: %+v %v, renewed %v", c, err, f.refreshed)
	}
	if saved, _, _ := LoadToken(brain); saved.Refresh != "refresh_2" || saved.Organization != "org-1" {
		t.Errorf("the renewed token was not saved: %+v", saved)
	}
	if exclude, _ := os.ReadFile(filepath.Join(brain, ".git", "info", "exclude")); strings.Count(string(exclude), "/linear.json") != 1 {
		t.Errorf("the token is not kept out of git: %q", exclude)
	}
	data, _ := os.ReadFile(filepath.Join(brain, TokenFile))
	if !IsToken(data) || IsToken([]byte(`{"access_token": "x"}`)) {
		t.Error("IsToken")
	}

	if err := o.Forget(ctx, brain); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(brain, TokenFile)); !os.IsNotExist(err) || len(f.revoked) != 2 {
		t.Errorf("forget left %v, revoked %v", err, f.revoked)
	}
	if _, ok, err := o.FromBrain(ctx, brain); ok || err != nil {
		t.Errorf("a brain without a token: %v %v", ok, err)
	}
}
