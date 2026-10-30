package dashboard

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/seal"
)

// Keyring is the brain's keyring on its server as a machine that holds the key sees it: the dashboard that machine
// serves of its decrypted copy approves other machines and lets the AIs that run in the cloud in with it.
type Keyring interface {
	Keys(ctx context.Context) (remote.Keys, error)
	Self() []byte
	Approve(ctx context.Context, req remote.KeyRequest) error
	LetCloudAIs(ctx context.Context) error
	StopCloudAIs(ctx context.Context) error
}

// encryptionInfo is the Encryption page: whether the brain is sealed, the machines that hold its key and those that
// wait for it, and whether the AIs that run in the cloud may read it, with what this dashboard can change.
type encryptionInfo struct {
	Sealed   bool
	Required bool
	Holds    bool
	CloudAIs bool
	Machines []keyMachine
	Waiting  []keyMachine
	Warning  string
}

type keyMachine struct {
	Name  string
	Code  string
	Since time.Time
	Self  bool
}

func (d *Dashboard) encryption(w http.ResponseWriter, r *http.Request, machine string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "encryption", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title = "Encryption"
	keys, self, err := d.keyring(r.Context(), machine)
	if err != nil {
		d.fail(w, err)
		return
	}
	e := &encryptionInfo{Sealed: keys.Sealed, Required: keys.Required, Holds: d.opts.Keyring != nil && keys.Wrapped != nil, CloudAIs: keys.CloudAIs}
	if q := r.URL.Query(); q.Get("done") == "changed" {
		e.Warning = q.Get("machine") + " asked again with another code, so it was not approved. Compare the new code below."
	} else {
		p.Notice = encryptionNotice(q)
	}
	for _, h := range keys.Holders {
		since, _ := time.Parse(time.RFC3339, h.Created)
		e.Machines = append(e.Machines, keyMachine{Name: h.Device, Since: since, Self: self != nil && bytes.Equal(h.Public, self)})
	}
	for _, p := range keys.Pending {
		since, _ := time.Parse(time.RFC3339, p.Created)
		e.Waiting = append(e.Waiting, keyMachine{Name: p.Device, Code: seal.Fingerprint(p.Public), Since: since})
	}
	p.Encryption = e
	d.render(w, "encryption", p)
}

// keyring returns the keyring as this dashboard sees it: through the machine that holds the key when it serves its
// copy, or as the server keeps it, for a signed-in browser that holds none.
func (d *Dashboard) keyring(ctx context.Context, machine string) (remote.Keys, []byte, error) {
	if d.opts.Keyring != nil {
		keys, err := d.opts.Keyring.Keys(ctx)
		return keys, d.opts.Keyring.Self(), err
	}
	keys, err := d.store.Keys(machine)
	return keys, nil, err
}

func (d *Dashboard) changeEncryption(w http.ResponseWriter, r *http.Request, machine string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	done := url.Values{}
	var err error
	switch k := d.opts.Keyring; {
	case r.Form.Get("do") == "cloud-off" && k != nil:
		err, done = k.StopCloudAIs(ctx), url.Values{"done": {"cloud-off"}}
	case r.Form.Get("do") == "cloud-off":
		err, done = d.store.StopCloudAIs(), url.Values{"done": {"cloud-off"}}
	case k == nil:
		http.Error(w, "this dashboard does not hold the brain's key: do it on a machine that does", http.StatusForbidden)
		return
	case r.Form.Get("do") == "cloud-on" && r.Form.Get("understood") == "yes":
		err, done = k.LetCloudAIs(ctx), url.Values{"done": {"cloud-on"}}
	case r.Form.Get("do") == "approve":
		err = d.approveKey(ctx, k, r.Form.Get("machine"), r.Form.Get("code"))
		done = url.Values{"done": {"approved"}, "machine": {r.Form.Get("machine")}}
	default:
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var stale *staleCode
	switch {
	case errors.As(err, &stale):
		done = url.Values{"done": {"changed"}, "machine": {stale.machine}}
	case err != nil:
		d.fail(w, err)
		return
	}
	d.log.Info("encryption", "do", r.Form.Get("do"), "by", machine)
	http.Redirect(w, r, "/encryption?"+done.Encode(), http.StatusSeeOther)
}

// staleCode is a machine that no longer waits with the code the page showed, so it is not approved.
type staleCode struct{ machine string }

func (e *staleCode) Error() string {
	return e.machine + " no longer waits with the code that was shown"
}

// approveKey gives the brain's key to the machine that waits with the code the person compared, and to no other.
func (d *Dashboard) approveKey(ctx context.Context, k Keyring, machine, code string) error {
	keys, err := k.Keys(ctx)
	if err != nil {
		return err
	}
	for _, p := range keys.Pending {
		if p.Device == machine && seal.Fingerprint(p.Public) == code {
			return k.Approve(ctx, p)
		}
	}
	return &staleCode{machine}
}

func encryptionNotice(q url.Values) string {
	switch q.Get("done") {
	case "approved":
		return q.Get("machine") + " has the brain's key now, and syncs on its next try."
	case "cloud-on":
		return "The AIs that run in the cloud can read the brain now."
	case "cloud-off":
		return "The server forgot the brain's key: the AIs that run in the cloud cannot read it any more."
	}
	return ""
}
