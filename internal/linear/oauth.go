package linear

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	linearWeb = "https://linear.app"
	linearAPI = "https://api.linear.app"

	// TokenFile is where, in the brain, its machines keep the token Connect Linear gave them.
	TokenFile = "linear.json"
)

// Token is what Linear's OAuth gives: a token that lasts a day, the one that renews it, the application it is for and
// the workspace it opens.
type Token struct {
	Access       string    `json:"access_token"`
	Refresh      string    `json:"refresh_token"`
	Expires      time.Time `json:"expires"`
	ClientID     string    `json:"client_id"`
	Organization string    `json:"organization,omitempty"`
	Name         string    `json:"name,omitempty"`
}

// OAuth is a Linear OAuth application used without its secret: PKCE stands for it, so the machines renew the token
// they hold on their own. Web and API are Linear's addresses, other than its own only in tests.
type OAuth struct {
	ClientID string
	Web, API string
	HTTP     *http.Client
}

func (o OAuth) client() *http.Client {
	return cmp.Or(o.HTTP, &http.Client{Timeout: 10 * time.Second})
}

func (o OAuth) api() string { return cmp.Or(o.API, linearAPI) }

// NewVerifier makes a PKCE verifier, and Challenge what Linear is told of it.
func NewVerifier() string {
	raw := make([]byte, 32)
	rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL sends a person to Linear to let wong read their workspace, coming back to redirect with state.
func (o OAuth) AuthorizeURL(redirect, state, verifier string) string {
	q := url.Values{"client_id": {o.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {"read"},
		"state": {state}, "prompt": {"consent"}, "actor": {"user"}, "code_challenge": {Challenge(verifier)}, "code_challenge_method": {"S256"}}
	return cmp.Or(o.Web, linearWeb) + "/oauth/authorize?" + q.Encode()
}

// Exchange trades the code Linear sent back for a token, and learns the workspace it opens.
func (o OAuth) Exchange(ctx context.Context, code, redirect, verifier string) (Token, error) {
	t, err := o.grant(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if err != nil {
		return Token{}, err
	}
	var out struct {
		Organization struct{ ID, Name string }
	}
	if err := o.with(t).query(ctx, `query { organization { id name } }`, nil, &out); err != nil {
		return Token{}, err
	}
	t.Organization, t.Name = out.Organization.ID, out.Organization.Name
	return t, nil
}

// Renew trades a token's refresh token for a new pair, which Linear rotates each time.
func (o OAuth) Renew(ctx context.Context, t Token) (Token, error) {
	o.ClientID = cmp.Or(t.ClientID, o.ClientID)
	n, err := o.grant(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	if err != nil {
		return Token{}, err
	}
	n.Organization, n.Name = t.Organization, t.Name
	return n, nil
}

// Revoke tells Linear to forget a token, and the refresh token that came with it.
func (o OAuth) Revoke(ctx context.Context, t Token) error {
	var errs []error
	for _, tok := range []struct{ value, hint string }{{t.Access, "access_token"}, {t.Refresh, "refresh_token"}} {
		if tok.value == "" {
			continue
		}
		resp, err := o.post(ctx, "/oauth/revoke", url.Values{"token": {tok.value}, "token_type_hint": {tok.hint}})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		resp.Body.Close()
	}
	return errors.Join(errs...)
}

func (o OAuth) grant(ctx context.Context, form url.Values) (Token, error) {
	form.Set("client_id", o.ClientID)
	resp, err := o.post(ctx, "/oauth/token", form)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err := json.Unmarshal(data, &out); err != nil || resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return Token{}, fmt.Errorf("linear: %s: %s", resp.Status, cmp.Or(out.Description, out.Error, strings.TrimSpace(string(data))))
	}
	return Token{Access: out.AccessToken, Refresh: out.RefreshToken, Expires: time.Now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second), ClientID: o.ClientID}, nil
}

func (o OAuth) post(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.api()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear: %w", err)
	}
	return resp, nil
}

// with is a client that reads Linear's API with a token of this application.
func (o OAuth) with(t Token) *Client {
	c := New(t.Access)
	c.Bearer = true
	c.URL = o.api() + "/graphql"
	c.HTTP = o.client()
	return c
}

// LoadToken reads the token a brain's machines keep, if Linear was connected.
func LoadToken(brain string) (Token, bool, error) {
	data, err := os.ReadFile(filepath.Join(brain, TokenFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Token{}, false, nil
	}
	if err != nil {
		return Token{}, false, err
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil || t.Access == "" {
		return Token{}, false, fmt.Errorf("%s does not hold a Linear token", TokenFile)
	}
	return t, true, nil
}

// IsToken reports whether data is a token file as SaveToken writes it, which the brain syncs although it holds a secret.
func IsToken(data []byte) bool {
	var t Token
	return json.Unmarshal(data, &t) == nil && t.Access != "" && t.ClientID != ""
}

// SaveToken writes the token a brain's machines keep, and keeps it out of the brain's git history.
func SaveToken(brain string, t Token) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := ignoreInGit(brain); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(brain, TokenFile), append(data, '\n'), 0o600)
}

func ignoreInGit(brain string) error {
	info, err := os.Stat(filepath.Join(brain, ".git"))
	if err != nil || !info.IsDir() {
		return nil
	}
	path := filepath.Join(brain, ".git", "info", "exclude")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	line := "/" + TokenFile
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, line+"\n"...), 0o644)
}

// FromBrain is the client a machine reads Linear with when the brain holds a token: renewed and saved again once it
// has less than ten minutes left, since Linear's tokens last a day. ok is false when Linear was never connected.
func (o OAuth) FromBrain(ctx context.Context, brain string) (c *Client, ok bool, err error) {
	t, ok, err := LoadToken(brain)
	if !ok || err != nil {
		return nil, ok, err
	}
	if time.Until(t.Expires) < 10*time.Minute {
		renewed, err := o.Renew(ctx, t)
		if err != nil {
			return nil, true, fmt.Errorf("Linear's token in the brain could not be renewed, so Linear issues go unchecked until another machine's sync brings a newer one: %w", err)
		}
		if err := SaveToken(brain, renewed); err != nil {
			return nil, true, err
		}
		t = renewed
	}
	return o.with(t), true, nil
}

// Forget revokes the brain's token with Linear and removes it, once its workspace is disconnected.
func (o OAuth) Forget(ctx context.Context, brain string) error {
	t, ok, err := LoadToken(brain)
	if !ok || err != nil {
		return err
	}
	revokeErr := o.Revoke(ctx, t)
	if err := os.Remove(filepath.Join(brain, TokenFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return revokeErr
}
