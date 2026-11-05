package remote

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	githubAPI = "https://api.github.com"
	githubWeb = "https://github.com"
	maxPRFile = 3000
)

// GitHubApp is the GitHub App a server takes webhooks from and posts checks with, as GitHub returned it when the app
// was made from wong's manifest.
type GitHubApp struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`

	// API and Web are GitHub's addresses, other than github.com's only in tests.
	API, Web string       `json:"-"`
	HTTP     *http.Client `json:"-"`

	key    *rsa.PrivateKey
	mu     sync.Mutex
	tokens map[int64]installToken
}

type installToken struct {
	token   string
	expires time.Time
}

// LoadGitHubApp reads the GitHub App a server uses, written by wong github-app.
func LoadGitHubApp(file string) (*GitHubApp, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var app GitHubApp
	if err := json.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := app.ready(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &app, nil
}

// SaveGitHubApp writes what GitHub returned for an app made from a manifest, readable by its owner alone.
func SaveGitHubApp(file string, data []byte) (*GitHubApp, error) {
	var app GitHubApp
	if err := json.Unmarshal(data, &app); err != nil {
		return nil, err
	}
	if err := app.ready(); err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return nil, err
	}
	return &app, nil
}

func (a *GitHubApp) ready() error {
	if a.ID == 0 || a.Slug == "" || a.ClientID == "" || a.ClientSecret == "" || a.WebhookSecret == "" || a.PEM == "" {
		return errors.New("a GitHub App needs its id, slug, client id and secret, webhook secret and private key")
	}
	block, _ := pem.Decode([]byte(a.PEM))
	if block == nil {
		return errors.New("the GitHub App's private key is not PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, err8 := x509.ParsePKCS8PrivateKey(block.Bytes)
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if err8 != nil || !ok {
			return fmt.Errorf("the GitHub App's private key: %w", err)
		}
		key = rsaKey
	}
	a.key = key
	return nil
}

func (a *GitHubApp) api() string { return cmp.Or(a.API, githubAPI) }
func (a *GitHubApp) web() string { return cmp.Or(a.Web, githubWeb) }

func (a *GitHubApp) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// InstallURL is where a person installs the app on their GitHub account or organization and picks its repos.
func (a *GitHubApp) InstallURL() string {
	return a.web() + "/apps/" + url.PathEscape(a.Slug) + "/installations/new"
}

// AuthorizeURL sends a person to GitHub to say who they are to the app, coming back to redirect with state.
func (a *GitHubApp) AuthorizeURL(redirect, state string) string {
	q := url.Values{"client_id": {a.ClientID}, "redirect_uri": {redirect}, "state": {state}}
	return a.web() + "/login/oauth/authorize?" + q.Encode()
}

// jwt signs the app's own token, good for nine minutes, which it trades for each installation's.
func (a *GitHubApp) jwt() (string, error) {
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": a.ClientID})
	if err != nil {
		return "", err
	}
	signed := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// token returns an installation's access token, made again a few minutes before the last one expires.
func (a *GitHubApp) token(ctx context.Context, installation int64) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[installation]; ok && time.Until(t.expires) > 5*time.Minute {
		return t.token, nil
	}
	jwt, err := a.jwt()
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := a.call(ctx, "Bearer "+jwt, http.MethodPost, "/app/installations/"+strconv.FormatInt(installation, 10)+"/access_tokens", nil, &out); err != nil {
		return "", err
	}
	if a.tokens == nil {
		a.tokens = map[int64]installToken{}
	}
	a.tokens[installation] = installToken{out.Token, out.ExpiresAt}
	return out.Token, nil
}

// as calls GitHub's API on behalf of an installation.
func (a *GitHubApp) as(ctx context.Context, installation int64, method, path string, in, out any) error {
	token, err := a.token(ctx, installation)
	if err != nil {
		return err
	}
	return a.call(ctx, "Bearer "+token, method, path, in, out)
}

func (a *GitHubApp) call(ctx context.Context, auth, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.api()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Installation is one installation of the app: the GitHub account or organization it is on.
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

// ManageURL is where the installation's repos are picked, and the app uninstalled.
func (i Installation) ManageURL(web string) string {
	id := strconv.FormatInt(i.ID, 10)
	if i.Account.Type == "Organization" {
		return cmp.Or(web, githubWeb) + "/organizations/" + url.PathEscape(i.Account.Login) + "/settings/installations/" + id
	}
	return cmp.Or(web, githubWeb) + "/settings/installations/" + id
}

// UserInstallations trades the code GitHub sent back for the person's token, and lists the installations of the app
// they can reach with it. The token is then dropped: it only says who came back.
func (a *GitHubApp) UserInstallations(ctx context.Context, code, redirect string) ([]Installation, error) {
	form := url.Values{"client_id": {a.ClientID}, "client_secret": {a.ClientSecret}, "code": {code}, "redirect_uri": {redirect}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.web()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		return nil, fmt.Errorf("github did not say who you are: %s", cmp.Or(tok.Error, resp.Status))
	}
	var all []Installation
	for page := 1; page <= 10; page++ {
		var out struct {
			Installations []Installation `json:"installations"`
		}
		if err := a.call(ctx, "Bearer "+tok.AccessToken, http.MethodGet, "/user/installations?per_page=100&page="+strconv.Itoa(page), nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Installations...)
		if len(out.Installations) < 100 {
			break
		}
	}
	return all, nil
}

// prFile is a file a pull request changes, as GitHub lists it: added, removed, modified, renamed and so on.
type prFile struct {
	Path     string `json:"path"`
	Status   string `json:"status"`
	Previous string `json:"previous,omitempty"`
}

// PRFiles lists the files a pull request changes, up to the 3000 GitHub returns.
func (a *GitHubApp) PRFiles(ctx context.Context, installation int64, repo string, number int) ([]prFile, error) {
	var files []prFile
	for page := 1; page <= maxPRFile/100; page++ {
		var out []struct {
			Filename         string `json:"filename"`
			Status           string `json:"status"`
			PreviousFilename string `json:"previous_filename"`
		}
		path := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, number, page)
		if err := a.as(ctx, installation, http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		for _, f := range out {
			files = append(files, prFile{Path: f.Filename, Status: f.Status, Previous: f.PreviousFilename})
		}
		if len(out) < 100 {
			break
		}
	}
	return files, nil
}

// checkRun is what a check on a commit says: queued while it waits, completed with its conclusion.
type checkRun struct {
	Name       string       `json:"name,omitempty"`
	HeadSHA    string       `json:"head_sha,omitempty"`
	Status     string       `json:"status"`
	Conclusion string       `json:"conclusion,omitempty"`
	Output     *checkOutput `json:"output,omitempty"`
}

type checkOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// CreateCheck posts wong's check on a commit of a repo and returns its id.
func (a *GitHubApp) CreateCheck(ctx context.Context, installation int64, repo string, run checkRun) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := a.as(ctx, installation, http.MethodPost, "/repos/"+repo+"/check-runs", run, &out)
	return out.ID, err
}

// UpdateCheck changes what one of wong's checks says.
func (a *GitHubApp) UpdateCheck(ctx context.Context, installation int64, repo string, id int64, run checkRun) error {
	return a.as(ctx, installation, http.MethodPatch, "/repos/"+repo+"/check-runs/"+strconv.FormatInt(id, 10), run, nil)
}

// ConvertManifest trades the code GitHub returns once it has made an app from a manifest for the app's credentials.
func ConvertManifest(ctx context.Context, api, code string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cmp.Or(api, githubAPI)+"/app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("github: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}
