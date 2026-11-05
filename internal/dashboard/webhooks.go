package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/17tayyy/wong/internal/linear"
	"github.com/17tayyy/wong/internal/remote"
)

// hookView is a webhook on the Webhooks page: where to point it, and the secret that signs it or the variable that sets
// it; with the server's app for it, where to install the app and what this brain is linked to.
type hookView struct {
	ID, Name, URL string
	Secret        string
	Env           string
	App           bool
	InstallURL    string
	Linked        []remote.Link
}

func (d *Dashboard) webhooks(w http.ResponseWriter, r *http.Request, machine string) {
	d.webhooksPage(w, r, machine, "")
}

func (d *Dashboard) webhooksPage(w http.ResponseWriter, r *http.Request, machine, problem string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "webhooks", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "Webhooks"
	p.Notice = notice(r)
	p.Error = problem
	where := "/hooks/"
	if d.account != "" {
		where += d.account + "/"
	}
	for _, id := range remote.Hooks {
		h := hookView{ID: id, Name: map[string]string{"github": "GitHub", "linear": "Linear"}[id], URL: origin(r) + where + id}
		env := map[string][2]string{"github": {d.opts.Webhooks.GitHubSecret, "WONG_GITHUB_SECRET"}, "linear": {d.opts.Webhooks.LinearSecret, "WONG_LINEAR_SECRET"}}[id]
		if d.account == "" && env[0] != "" {
			h.Env = env[1]
		} else if h.Secret, err = d.store.HookSecret(id); err != nil {
			d.fail(w, err)
			return
		}
		if app := d.opts.Webhooks.App; id == "github" && app != nil && d.opts.Webhooks.Links != nil {
			h.App, h.InstallURL, h.Linked = true, app.InstallURL(), d.opts.Webhooks.Links.Of("github", d.account)
		}
		if id == "linear" && d.opts.Webhooks.Linear != nil && d.opts.Webhooks.Links != nil {
			h.App, h.Linked = true, d.opts.Webhooks.Links.Of("linear", d.account)
		}
		p.Hooks = append(p.Hooks, h)
	}
	d.render(w, "webhooks", p)
}

// setWebhook makes a secret for GitHub's webhook, keeps the one Linear shows, or turns a webhook off.
func (d *Dashboard) setWebhook(w http.ResponseWriter, r *http.Request, _ string) {
	hook := r.PathValue("hook")
	if !slices.Contains(remote.Hooks, hook) {
		http.NotFound(w, r)
		return
	}
	var secret, done string
	switch r.FormValue("do") {
	case "generate":
		raw := make([]byte, 32)
		rand.Read(raw)
		secret, done = hex.EncodeToString(raw), "hook-new"
	case "save":
		if secret, done = strings.TrimSpace(r.FormValue("secret")), "hook-saved"; secret == "" {
			http.Error(w, "paste the secret first", http.StatusBadRequest)
			return
		}
	case "off":
		done = "hook-off"
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err := d.store.SetHookSecret(hook, secret); err != nil {
		d.fail(w, err)
		return
	}
	http.Redirect(w, r, "/webhooks?done="+done, http.StatusSeeOther)
}

// connects holds the sign-ins with GitHub or Linear that have left for their site, until they come back.
type connects struct {
	mu sync.Mutex
	m  map[string]connect
}

// connect is one of them: the account it links, the installation GitHub said was installed, if any, and the PKCE
// verifier of a sign-in with Linear.
type connect struct {
	account      string
	installation int64
	verifier     string
	expires      time.Time
}

func (c *connects) start(cn connect) string {
	raw := make([]byte, 24)
	rand.Read(raw)
	state := hex.EncodeToString(raw)
	cn.expires = time.Now().Add(15 * time.Minute)
	c.mu.Lock()
	defer c.mu.Unlock()
	for s, old := range c.m {
		if time.Now().After(old.expires) {
			delete(c.m, s)
		}
	}
	c.m[state] = cn
	return state
}

// take returns the sign-in a state stands for, once, while it is fresh and only to the account that started it.
func (c *connects) take(state, account string) (connect, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cn, ok := c.m[state]
	delete(c.m, state)
	return cn, ok && cn.account == account && time.Now().Before(cn.expires)
}

// githubSetup is where GitHub sends a person after they install the app or change its repos. GitHub's word on which
// installation it was is not proof, so the person signs in with GitHub, which says which installations they can reach.
func (d *Dashboard) githubSetup(w http.ResponseWriter, r *http.Request, _ string) {
	app := d.opts.Webhooks.App
	if app == nil || d.opts.Webhooks.Links == nil {
		http.NotFound(w, r)
		return
	}
	installation, _ := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	state := d.connects.start(connect{account: d.account, installation: installation})
	http.Redirect(w, r, app.AuthorizeURL(origin(r)+"/webhooks/github/callback", state), http.StatusSeeOther)
}

// githubCallback links the installation GitHub said was installed, or every one the person can reach when they came to
// link those they had, once GitHub confirms they can reach it.
func (d *Dashboard) githubCallback(w http.ResponseWriter, r *http.Request, machine string) {
	app, links := d.opts.Webhooks.App, d.opts.Webhooks.Links
	if app == nil || links == nil {
		http.NotFound(w, r)
		return
	}
	cn, ok := d.connects.take(r.URL.Query().Get("state"), d.account)
	if !ok {
		d.webhooksPage(w, r, machine, "That link to GitHub expired or was used already. Connect GitHub again.")
		return
	}
	installs, err := app.UserInstallations(r.Context(), r.URL.Query().Get("code"), origin(r)+"/webhooks/github/callback")
	if err != nil {
		d.log.Warn("github sign-in", "err", err)
		d.webhooksPage(w, r, machine, "GitHub did not say who you are, so wong linked nothing. Connect GitHub again.")
		return
	}
	linked := 0
	for _, in := range installs {
		if cn.installation != 0 && in.ID != cn.installation {
			continue
		}
		err := links.Add(remote.Link{Kind: "github", ID: strconv.FormatInt(in.ID, 10), Account: d.account, Name: in.Account.Login, URL: in.ManageURL(app.Web)})
		if err != nil {
			d.fail(w, err)
			return
		}
		linked++
	}
	if linked == 0 {
		d.webhooksPage(w, r, machine, "Your GitHub user cannot reach that installation of the app, so wong linked nothing. Install the app on an account or organization you manage.")
		return
	}
	http.Redirect(w, r, "/webhooks?done=github-linked", http.StatusSeeOther)
}

// githubLink sends a person who installed the app before to GitHub, to link the installations they can reach.
func (d *Dashboard) githubLink(w http.ResponseWriter, r *http.Request, machine string) {
	r.URL.RawQuery = ""
	d.githubSetup(w, r, machine)
}

// linearConnect sends a person to Linear to let wong read their workspace.
func (d *Dashboard) linearConnect(w http.ResponseWriter, r *http.Request, _ string) {
	app := d.opts.Webhooks.Linear
	if app == nil || d.opts.Webhooks.Links == nil {
		http.NotFound(w, r)
		return
	}
	verifier := linear.NewVerifier()
	state := d.connects.start(connect{account: d.account, verifier: verifier})
	http.Redirect(w, r, app.OAuth.AuthorizeURL(origin(r)+"/webhooks/linear/callback", state, verifier), http.StatusSeeOther)
}

// linearCallback links the workspace a person let wong into, and hands its token to the brain's machines.
func (d *Dashboard) linearCallback(w http.ResponseWriter, r *http.Request, machine string) {
	app, links := d.opts.Webhooks.Linear, d.opts.Webhooks.Links
	if app == nil || links == nil {
		http.NotFound(w, r)
		return
	}
	cn, ok := d.connects.take(r.URL.Query().Get("state"), d.account)
	switch {
	case !ok:
		d.webhooksPage(w, r, machine, "That link to Linear expired or was used already. Connect Linear again.")
		return
	case r.URL.Query().Get("error") != "":
		d.webhooksPage(w, r, machine, "Linear did not let wong in, so nothing changed.")
		return
	}
	err := remote.ConnectLinear(r.Context(), app, links, d.store, d.account, r.URL.Query().Get("code"), origin(r)+"/webhooks/linear/callback", cn.verifier)
	if err != nil {
		d.log.Warn("linear connect", "err", err)
		d.webhooksPage(w, r, machine, "Linear did not hand wong a token, so nothing changed. Connect Linear again.")
		return
	}
	http.Redirect(w, r, "/webhooks?done=linear-linked", http.StatusSeeOther)
}

// linearUnlink disconnects the workspace: its webhooks stop reaching this brain, and the machines revoke the token.
func (d *Dashboard) linearUnlink(w http.ResponseWriter, r *http.Request, _ string) {
	links := d.opts.Webhooks.Links
	if links == nil {
		http.NotFound(w, r)
		return
	}
	if err := links.Remove("linear", r.FormValue("id"), d.account); err != nil {
		d.fail(w, err)
		return
	}
	if err := d.store.HandOffForget(); err != nil {
		d.fail(w, err)
		return
	}
	http.Redirect(w, r, "/webhooks?done=linear-unlinked", http.StatusSeeOther)
}

// githubUnlink stops an installation's webhooks from reaching this brain; the app stays installed on GitHub.
func (d *Dashboard) githubUnlink(w http.ResponseWriter, r *http.Request, _ string) {
	links := d.opts.Webhooks.Links
	if links == nil {
		http.NotFound(w, r)
		return
	}
	if err := links.Remove("github", r.FormValue("id"), d.account); err != nil {
		d.fail(w, err)
		return
	}
	http.Redirect(w, r, "/webhooks?done=github-unlinked", http.StatusSeeOther)
}
