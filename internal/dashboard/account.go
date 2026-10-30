package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/remote"
)

// AccountService is the sign-up service that knows each account's plan; the Account page asks it what to say, at URL
// with {account} replaced by the account, with the admin token the service also uses to call this server.
type AccountService struct {
	URL   string
	Token string
}

// accountView is what an account service says about one account: a sentence on where it stands, an optional notice,
// the plan it has, the plans it can take, where its payment is managed, and a closing note.
type accountView struct {
	Tone     string  `json:"tone"`
	Headline string  `json:"headline"`
	Detail   string  `json:"detail"`
	Notice   *offer  `json:"notice"`
	Plan     *offer  `json:"plan"`
	Offers   []offer `json:"offers"`
	Manage   *offer  `json:"manage"`
	Note     string  `json:"note"`
}

// offer is a plan, a notice or an action: a name, a line under it, and a link with its label when there is one.
type offer struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Action string `json:"action"`
	URL    string `json:"url"`
}

// describe asks the service about account, passing the page's query and the visitor's country when the proxy in
// front of this server gives it.
func (s *AccountService) describe(ctx context.Context, account string, q url.Values) (*accountView, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u := strings.ReplaceAll(s.URL, "{account}", url.PathEscape(account))
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the account service answered %s", res.Status)
	}
	var v accountView
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// accountInfo is the Account page: the plan as the account service tells it, the brain's room and machines, and the
// address MCP clients connect to.
type accountInfo struct {
	View     *accountView
	Down     bool
	Account  string
	Storage  string
	Machines []machineSeen
	MCP      string
}

func (d *Dashboard) accountPage(w http.ResponseWriter, r *http.Request, machine string) {
	if d.account == "" {
		http.NotFound(w, r)
		return
	}
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "account", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "Account"
	a := &accountInfo{Account: d.account, Machines: p.Masthead.Machines, MCP: origin(r) + "/mcp"}
	size, err := d.store.Size()
	if err != nil {
		d.fail(w, err)
		return
	}
	a.Storage = remote.Bytes(size)
	if limit := d.store.Limits().Size; limit > 0 {
		a.Storage += " of " + remote.Bytes(limit)
	}
	q := r.URL.Query()
	if country := r.Header.Get("Cf-Ipcountry"); country != "" {
		q.Set("country", country)
	}
	if a.View, err = d.opts.Account.describe(r.Context(), d.account, q); err != nil {
		d.log.Warn("account service", "account", d.account, "err", err)
		a.Down = true
	}
	p.AccountPage = a
	d.render(w, "account", p)
}

func origin(r *http.Request) string {
	if secure(r) {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}
