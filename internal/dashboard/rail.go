package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/17tayyy/wong/internal/remote"
)

const railEvery = time.Minute

// Rail is what a server says of itself to a dashboard that runs elsewhere, in a browser or on a machine, from a sealed
// brain's decrypted copy, which holds the notes but not the server's accounts and tokens: whether it has an Account
// page, the page that says whether it works, the machines with a token, the account's standing and how much room
// the brain takes.
type Rail struct {
	Account   bool            `json:"account"`
	StatusURL string          `json:"status_url,omitempty"`
	Machines  []RailMachine   `json:"machines"`
	Standing  remote.Standing `json:"standing"`
	Storage   string          `json:"storage,omitempty"`
}

// RailMachine is a machine with a token of the brain, and when it last used it.
type RailMachine struct {
	Name string    `json:"name"`
	Seen time.Time `json:"seen,omitzero"`
}

// Upstream is the server a dashboard that runs elsewhere stands in for. The dashboard asks it for its Rail at most once
// a minute, with a token of the brain, and links its Account page there.
type Upstream struct {
	URL   string
	Token string
	HTTP  *http.Client

	mu   sync.Mutex
	last time.Time
	rail Rail
}

func (u *Upstream) get(ctx context.Context) Rail {
	u.mu.Lock()
	defer u.mu.Unlock()
	if time.Since(u.last) < railEvery {
		return u.rail
	}
	u.last = time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(u.URL, "/")+"/browser/rail", nil)
	if err != nil {
		return u.rail
	}
	req.Header.Set("Authorization", "Bearer "+u.Token)
	client := u.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return u.rail
	}
	defer resp.Body.Close()
	var rail Rail
	if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&rail) == nil {
		u.rail = rail
	}
	return u.rail
}

// railJSON tells a dashboard that runs elsewhere what this server's rail holds, to a browser signed in or a machine
// with a token of the brain.
func (d *Dashboard) railJSON(w http.ResponseWriter, r *http.Request) {
	var s remote.Session
	var err error
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		s, err = d.brains.Auth(token)
	} else if c, cerr := r.Cookie(cookieName); cerr == nil {
		s, err = d.brains.Visit(c.Value)
	}
	if err != nil {
		d.fail(w, err)
		return
	}
	if s.Store == nil {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	tokens, err := s.Store.Tokens()
	if err != nil {
		d.fail(w, err)
		return
	}
	rail := Rail{Account: d.opts.Account != nil && s.Account != "", StatusURL: d.opts.StatusURL, Machines: []RailMachine{}}
	if rail.Standing, err = s.Store.Standing(); err != nil {
		d.fail(w, err)
		return
	}
	if rail.Standing.Moved, err = s.Store.MovedTo(); err != nil {
		d.fail(w, err)
		return
	}
	if rail.Storage, err = storage(s.Store); err != nil {
		d.fail(w, err)
		return
	}
	for _, t := range tokens {
		seen, _ := time.Parse(time.RFC3339, t.LastUsed)
		rail.Machines = append(rail.Machines, RailMachine{t.Name, seen})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(rail)
}
