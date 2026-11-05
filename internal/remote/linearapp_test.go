package remote

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/seal"
)

const linearAppSecret = "linear-app-secret"

// fakeLinearAPI hands out a token for any code and names the workspace it opens, and records what is revoked.
func fakeLinearAPI(t *testing.T) (string, *[]string) {
	t.Helper()
	var revoked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/oauth/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "lin_oauth_secret", "refresh_token": "lin_refresh_secret", "expires_in": 86399})
		case "/oauth/revoke":
			revoked = append(revoked, r.Form.Get("token"))
		case "/graphql":
			w.Write([]byte(`{"data": {"organization": {"id": "org-acme", "name": "Acme"}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &revoked
}

func linearAppSetup(t *testing.T) (*Store, *Links, *LinearApp, string, machine, *[]string) {
	t.Helper()
	api, revoked := fakeLinearAPI(t)
	app := &LinearApp{OAuth: linear.OAuth{ClientID: "client", API: api}, WebhookSecret: linearAppSecret}
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	links, err := OpenLinks(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Webhooks{Linear: app, Links: links}))
	t.Cleanup(srv.Close)
	token, err := store.NewToken("desktop")
	if err != nil {
		t.Fatal(err)
	}
	m := machine{brain: t.TempDir(), client: NewClient(srv.URL, token, 5*time.Second)}
	m.client.Linear = app.OAuth
	return store, links, app, srv.URL, m, revoked
}

func TestLinearsTokenReachesASealedBrainsMachinesSealed(t *testing.T) {
	ctx := context.Background()
	store, links, app, _, desktop, _ := linearAppSetup(t)
	desktop.client.Device = newDevice(t)
	key, _ := seal.NewKey()
	recovery, _ := seal.RecoveryDevice(seal.NewRecovery())
	if err := desktop.client.StartSeal(ctx, desktop.client.Device, recovery, key, 0); err != nil {
		t.Fatal(err)
	}

	if err := ConnectLinear(ctx, app, links, store, "", "the-code", "https://wong.test/webhooks/linear/callback", "v"); err != nil {
		t.Fatal(err)
	}
	if got := links.Of("linear", ""); len(got) != 1 || got[0].ID != "org-acme" || got[0].Name != "Acme" {
		t.Fatalf("links = %+v", got)
	}
	var blob []byte
	if err := store.db.QueryRow(`SELECT blob FROM handoffs WHERE device = 'desktop'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("lin_oauth_secret")) {
		t.Fatal("the server keeps Linear's token in the clear for a sealed brain")
	}

	if _, err := OpenTasks(ctx, desktop.client, desktop.brain, "acme"); err != nil {
		t.Fatal(err)
	}
	tok, ok, err := linear.LoadToken(desktop.brain)
	if err != nil || !ok || tok.Access != "lin_oauth_secret" || tok.Organization != "org-acme" || tok.ClientID != "client" {
		t.Fatalf("the desktop's token = %+v %v %v", tok, ok, err)
	}
	var left int
	store.db.QueryRow(`SELECT COUNT(*) FROM handoffs`).Scan(&left)
	if left != 0 {
		t.Errorf("the server still holds %d handoffs once a machine took the token", left)
	}
	if res := push(t, desktop); len(res.Refused) != 0 || len(res.Pushed) != 1 {
		t.Fatalf("pushing the token: %+v", res)
	}
	var stored []byte
	store.db.QueryRow(`SELECT content FROM files`).Scan(&stored)
	if bytes.Contains(stored, []byte("lin_oauth_secret")) {
		t.Error("the token reached the server in the clear")
	}
}

func TestLinearsAppWebhooksReachTheLinkedBrain(t *testing.T) {
	ctx := context.Background()
	store, links, app, url, desktop, revokedTokens := linearAppSetup(t)
	writeAt(t, filepath.Join(desktop.brain, "projects/acme/d.md"), "---\nname: d\ndescription: d\n---\nENG-15 is in progress.\n", "2026-10-01")
	push(t, desktop)
	payload := func(org string) string {
		return strings.Replace(linearPayload("Done", "completed", time.Now()), `"type": "Issue"`, fmt.Sprintf(`"type": "Issue", "organizationId": %q`, org), 1)
	}
	send := func(body string) int {
		return deliver(t, url+"/hooks/linear-app", map[string]string{"Linear-Signature": sign(linearAppSecret, body)}, body)
	}

	if code := send(payload("org-acme")); code != http.StatusOK || len(tasks(t, desktop, "acme")) != 0 {
		t.Fatalf("a workspace linked to no brain: %d, %+v", code, tasks(t, desktop, "acme"))
	}
	if err := ConnectLinear(ctx, app, links, store, "", "the-code", "https://wong.test/webhooks/linear/callback", "v"); err != nil {
		t.Fatal(err)
	}
	if code := send(payload("org-other")); code != http.StatusOK || len(tasks(t, desktop, "acme")) != 0 {
		t.Fatalf("another workspace's issue: %d", code)
	}
	if code := send(payload("org-acme")); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got := tasks(t, desktop, "acme"); len(got) != 1 || got[0].Ref != "ENG-15" {
		t.Fatalf("tasks = %+v", got)
	}

	if _, err := OpenTasks(ctx, desktop.client, desktop.brain, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := linear.LoadToken(desktop.brain); !ok {
		t.Fatal("a brain in the clear's machine did not take the token")
	}
	revoked := fmt.Sprintf(`{"action": "revoked", "type": "OAuthApp", "organizationId": "org-acme", "oauthClientId": "client", "webhookTimestamp": %d}`, time.Now().UnixMilli())
	if code := send(revoked); code != http.StatusOK {
		t.Fatalf("revoked: status %d", code)
	}
	if got := links.Of("linear", ""); len(got) != 0 {
		t.Errorf("a revoked workspace is still linked: %+v", got)
	}
	if _, err := OpenTasks(ctx, desktop.client, desktop.brain, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(desktop.brain, linear.TokenFile)); !os.IsNotExist(err) {
		t.Errorf("the token outlived the revoked workspace: %v", err)
	}
	if got := *revokedTokens; len(got) != 2 || got[0] != "lin_oauth_secret" || got[1] != "lin_refresh_secret" {
		t.Errorf("revoked with Linear: %v", got)
	}
	if err := store.db.QueryRow(`SELECT kind FROM handoffs`).Scan(new(string)); err != sql.ErrNoRows {
		t.Errorf("the word to forget the token is still held: %v", err)
	}
}
