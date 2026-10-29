package remote

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	codeTTL   = time.Minute
	maxCodes  = 1024
	maxClient = 2048
)

// OAuth lets MCP clients such as claude.ai connect to a brain as they expect to: they register, send the person to
// approve them on the dashboard, and trade the code they get back for a token of that brain. A client's registration
// travels signed inside its id, so the server keeps none.
type OAuth struct {
	key   []byte
	mu    sync.Mutex
	codes map[string]grant
	now   func() time.Time
}

// OAuthClient is an MCP client as it registered: its name and where codes may be sent.
type OAuthClient struct {
	Name      string   `json:"client_name"`
	Redirects []string `json:"redirect_uris"`
}

type grant struct {
	store     *Store
	client    string
	redirect  string
	challenge string
	name      string
	made      time.Time
}

// NewOAuth uses the key in keyFile to sign client ids, making one the first time.
func NewOAuth(keyFile string) (*OAuth, error) {
	key, err := os.ReadFile(keyFile)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			return nil, err
		}
		err = os.WriteFile(keyFile, key, 0o600)
	}
	if err != nil {
		return nil, err
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("%s: too short to sign with", keyFile)
	}
	return &OAuth{key: key, codes: map[string]grant{}, now: time.Now}, nil
}

func (o *OAuth) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, o.key)
	mac.Write(data)
	return mac.Sum(nil)
}

func (o *OAuth) clientID(c OAuthClient) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(data) + "." + enc.EncodeToString(o.sign(data)), nil
}

// Client reads the registration a client id carries, refusing one this server did not sign.
func (o *OAuth) Client(id string) (OAuthClient, bool) {
	body, sig, ok := strings.Cut(id, ".")
	enc := base64.RawURLEncoding
	data, err1 := enc.DecodeString(body)
	mac, err2 := enc.DecodeString(sig)
	var c OAuthClient
	if !ok || len(id) > maxClient || err1 != nil || err2 != nil || !hmac.Equal(mac, o.sign(data)) || json.Unmarshal(data, &c) != nil {
		return OAuthClient{}, false
	}
	return c, true
}

// Grant makes the one-time code a client trades for a token of a brain, with the PKCE challenge it sent.
func (o *OAuth) Grant(store *Store, clientID, redirect, challenge string) (string, error) {
	c, ok := o.Client(clientID)
	if !ok {
		return "", errors.New("unknown client")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prune()
	if len(o.codes) >= maxCodes {
		return "", errors.New("too many codes are waiting")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw)
	o.codes[code] = grant{store: store, client: clientID, redirect: redirect, challenge: challenge, name: MachineName(c.Name), made: o.now()}
	return code, nil
}

func (o *OAuth) redeem(code, clientID, redirect, verifier string) (grant, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prune()
	g, ok := o.codes[code]
	if !ok {
		return grant{}, errors.New("the code is unknown, used or expired")
	}
	delete(o.codes, code)
	sum := sha256.Sum256([]byte(verifier))
	switch {
	case g.client != clientID:
		return grant{}, errors.New("the code was made for another client")
	case g.redirect != redirect:
		return grant{}, errors.New("redirect_uri differs from the one the code was sent to")
	case subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(g.challenge)) != 1:
		return grant{}, errors.New("code_verifier does not match the code_challenge")
	}
	return g, nil
}

func (o *OAuth) prune() {
	for code, g := range o.codes {
		if o.now().Sub(g.made) > codeTTL {
			delete(o.codes, code)
		}
	}
}

// ValidRedirect accepts where a client may receive codes: https, or http on this computer for a local client.
func ValidRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" || len(raw) > 512 {
		return false
	}
	host := u.Hostname()
	return u.Scheme == "https" || u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")
}

type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
}

// OAuthHandler serves what an MCP client finds and calls on its own: the metadata of the MCP resource and of this
// authorization server, client registration, and the token endpoint. Approving a client is the dashboard's.
func OAuthHandler(o *OAuth, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	resource := func(w http.ResponseWriter, r *http.Request) {
		base := baseURL(r)
		writeJSON(w, map[string]any{
			"resource":                 base + "/mcp",
			"authorization_servers":    []string{base},
			"scopes_supported":         []string{"brain"},
			"bearer_methods_supported": []string{"header"},
			"resource_name":            "wong",
		})
	}
	server := func(w http.ResponseWriter, r *http.Request) {
		base := baseURL(r)
		writeJSON(w, map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/oauth/authorize",
			"token_endpoint":                        base + "/oauth/token",
			"registration_endpoint":                 base + "/oauth/register",
			"scopes_supported":                      []string{"brain"},
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	}
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		mux.HandleFunc("GET "+p, public(resource))
	}
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		mux.HandleFunc("GET "+p, public(server))
	}
	for _, p := range []string{"/oauth/register", "/oauth/token"} {
		mux.HandleFunc("OPTIONS "+p, public(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	mux.HandleFunc("POST /oauth/register", public(func(w http.ResponseWriter, r *http.Request) {
		var c OAuthClient
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&c); err != nil {
			oauthFail(w, http.StatusBadRequest, "invalid_client_metadata", "the registration is not valid JSON")
			return
		}
		if len(c.Redirects) == 0 || len(c.Redirects) > 8 || slices.ContainsFunc(c.Redirects, func(u string) bool { return !ValidRedirect(u) }) {
			oauthFail(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be https, or http on localhost, without a fragment")
			return
		}
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			c.Name = "MCP client"
		}
		if len([]rune(c.Name)) > 60 {
			c.Name = string([]rune(c.Name)[:60])
		}
		id, err := o.clientID(c)
		if err != nil {
			log.Error("oauth", "err", err)
			oauthFail(w, http.StatusInternalServerError, "server_error", "")
			return
		}
		log.Info("oauth client registered", "name", c.Name)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id": id, "client_id_issued_at": o.now().Unix(), "client_name": c.Name, "redirect_uris": c.Redirects,
			"grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none",
		})
	}))
	mux.HandleFunc("POST /oauth/token", public(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if r.FormValue("grant_type") != "authorization_code" {
			oauthFail(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
			return
		}
		g, err := o.redeem(r.FormValue("code"), r.FormValue("client_id"), r.FormValue("redirect_uri"), r.FormValue("code_verifier"))
		if err != nil {
			log.Info("oauth token refused", "reason", err)
			oauthFail(w, http.StatusBadRequest, "invalid_grant", err.Error())
			return
		}
		token, err := g.store.NewToken(g.name)
		if err != nil {
			log.Error("oauth", "err", err)
			oauthFail(w, http.StatusInternalServerError, "server_error", "")
			return
		}
		log.Info("oauth client connected", "machine", Session{Account: g.store.id, Machine: g.name}.Who())
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"access_token": token, "token_type": "Bearer", "scope": "brain"})
	}))
	return mux
}

// public lets any web page call an endpoint without credentials, as MCP clients in a browser do.
func public(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next(w, r)
	}
}

func oauthFail(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(oauthError{code, description})
}
