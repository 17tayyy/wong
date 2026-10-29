package dashboard

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/17tayyy/wong/internal/remote"
)

// connection is a client asking to use this brain, as the approval page shows it.
type connection struct {
	Client    string
	Host      string
	Name      string
	Taken     bool
	Params    url.Values
	ClientID  string
	Redirect  string
	State     string
	Challenge string
}

// authorize asks the person signed in whether an MCP client may use their brain, and sends the client a code if so.
func (d *Dashboard) authorize(w http.ResponseWriter, r *http.Request, machine string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	f := r.Form
	clientID, redirect, state := f.Get("client_id"), f.Get("redirect_uri"), f.Get("state")
	client, ok := d.opts.OAuth.Client(clientID)
	if !ok || !slices.Contains(client.Redirects, redirect) {
		w.WriteHeader(http.StatusBadRequest)
		d.authorizePage(w, r, machine, nil, "This request to connect is not valid: the app that sent you here is not registered with this server, or asked to answer somewhere it did not register. Start again from the app.")
		return
	}
	back := func(params url.Values) {
		u, _ := url.Parse(redirect)
		q := u.Query()
		for k, v := range params {
			q[k] = v
		}
		if state != "" {
			q.Set("state", state)
		}
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	switch {
	case f.Get("response_type") != "code":
		back(url.Values{"error": {"unsupported_response_type"}})
		return
	case f.Get("code_challenge") == "" || f.Get("code_challenge_method") != "S256":
		back(url.Values{"error": {"invalid_request"}, "error_description": {"PKCE with S256 is required"}})
		return
	}
	if r.Method == http.MethodPost {
		if f.Get("do") != "approve" {
			back(url.Values{"error": {"access_denied"}})
			return
		}
		code, err := d.opts.OAuth.Grant(d.store, clientID, redirect, f.Get("code_challenge"))
		if err != nil {
			d.fail(w, err)
			return
		}
		d.log.Info("oauth client approved", "client", client.Name, "by", machine)
		back(url.Values{"code": {code}, "iss": {remote.BaseURL(r)}})
		return
	}
	u, _ := url.Parse(redirect)
	c := &connection{Client: client.Name, Host: u.Host, Name: remote.MachineName(client.Name), ClientID: clientID, Redirect: redirect,
		State: state, Challenge: f.Get("code_challenge")}
	var err error
	if c.Taken, err = d.tokenNamed(c.Name); err != nil {
		d.fail(w, err)
		return
	}
	d.authorizePage(w, r, machine, c, "")
}

func (d *Dashboard) authorizePage(w http.ResponseWriter, r *http.Request, machine string, c *connection, problem string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title, p.Connection, p.Error = "Connect an app", c, problem
	d.render(w, "authorize", p)
}
