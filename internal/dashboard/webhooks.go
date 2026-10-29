package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"

	"github.com/17tayyy/wong/internal/remote"
)

// hookView is a webhook on the Webhooks page: where to point it, and the secret that signs it or the variable that sets it.
type hookView struct {
	ID, Name, URL string
	Secret        string
	Env           string
}

func (d *Dashboard) webhooks(w http.ResponseWriter, r *http.Request, machine string) {
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
	scheme := "http"
	if secure(r) {
		scheme = "https"
	}
	where := "/hooks/"
	if d.account != "" {
		where += d.account + "/"
	}
	for _, id := range remote.Hooks {
		h := hookView{ID: id, Name: map[string]string{"github": "GitHub", "linear": "Linear"}[id], URL: scheme + "://" + r.Host + where + id}
		env := map[string][2]string{"github": {d.env.GitHubSecret, "WONG_GITHUB_SECRET"}, "linear": {d.env.LinearSecret, "WONG_LINEAR_SECRET"}}[id]
		if d.account == "" && env[0] != "" {
			h.Env = env[1]
		} else if h.Secret, err = d.store.HookSecret(id); err != nil {
			d.fail(w, err)
			return
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
