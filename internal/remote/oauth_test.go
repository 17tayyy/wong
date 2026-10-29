package remote

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestOAuthConnectsAClientToABrain(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o, err := NewOAuth(filepath.Join(dir, "oauth-key"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(OAuthHandler(o, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)

	var meta map[string]any
	getJSON(t, srv.URL+"/.well-known/oauth-protected-resource/mcp", &meta)
	if meta["resource"] != srv.URL+"/mcp" || meta["authorization_servers"].([]any)[0] != srv.URL {
		t.Errorf("resource metadata = %v", meta)
	}
	getJSON(t, srv.URL+"/.well-known/oauth-authorization-server", &meta)
	if meta["registration_endpoint"] != srv.URL+"/oauth/register" || meta["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Errorf("server metadata = %v", meta)
	}

	resp, err := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(`{"client_name":"Claude","redirect_uris":["https://claude.ai/api/mcp/auth_callback"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()
	id, _ := reg["client_id"].(string)
	if resp.StatusCode != http.StatusCreated || id == "" {
		t.Fatalf("register: %d %v", resp.StatusCode, reg)
	}
	if c, ok := o.Client(id); !ok || c.Name != "Claude" {
		t.Errorf("Client = %+v, %v", c, ok)
	}
	if _, ok := o.Client(strings.Replace(id, "Q", "R", 1) + "x"); ok {
		t.Error("a client id changed by hand was accepted")
	}
	again, _ := NewOAuth(filepath.Join(dir, "oauth-key"))
	if _, ok := again.Client(id); !ok {
		t.Error("a client id stopped working when the server restarted")
	}
	if resp, _ := http.Post(srv.URL+"/oauth/register", "application/json", strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a plain-http redirect elsewhere was registered: %d", resp.StatusCode)
	}

	verifier := "a-verifier-of-enough-length-to-count-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "https://claude.ai/api/mcp/auth_callback"
	token := func(code, verifier, redirect string) (int, map[string]any) {
		t.Helper()
		resp, err := http.PostForm(srv.URL+"/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code},
			"client_id": {id}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, _ := o.Grant(store, id, redirect, challenge)
	if status, out := token(code, "wrong-verifier", redirect); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("a wrong verifier: %d %v", status, out)
	}
	code, _ = o.Grant(store, id, redirect, challenge)
	if status, _ := token(code, verifier, "https://claude.ai/elsewhere"); status != http.StatusBadRequest {
		t.Errorf("another redirect_uri: %d", status)
	}
	code, _ = o.Grant(store, id, redirect, challenge)
	status, out := token(code, verifier, redirect)
	access, _ := out["access_token"].(string)
	if status != http.StatusOK || out["token_type"] != "Bearer" || access == "" {
		t.Fatalf("token: %d %v", status, out)
	}
	if s, _ := store.Auth(access); s.Machine != "claude" {
		t.Errorf("the token opens %+v", s)
	}
	if status, _ := token(code, verifier, redirect); status != http.StatusBadRequest {
		t.Errorf("a code used twice: %d", status)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("%s lacks CORS", url)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
