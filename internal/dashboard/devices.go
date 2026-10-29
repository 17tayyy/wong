package dashboard

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/17tayyy/wong/internal/remote"
)

const maxWrongCodes = 10

// guesses keeps each token to a few wrong codes, so no one can try codes until one lets a stranger's machine into their brain.
type guesses struct {
	mu    sync.Mutex
	wrong map[string][]time.Time
}

func (g *guesses) allowed(who string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	since := time.Now().Add(-10 * time.Minute)
	g.wrong[who] = slices.DeleteFunc(g.wrong[who], func(t time.Time) bool { return t.Before(since) })
	return len(g.wrong[who]) < maxWrongCodes
}

func (g *guesses) miss(who string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.wrong[who] = append(g.wrong[who], time.Now())
}

func (d *Dashboard) device(w http.ResponseWriter, r *http.Request, machine string) {
	d.devicePage(w, r, machine, nil, "", "")
}

// approveDevice finds the machine waiting with a code, then lets it into this brain or turns it away.
func (d *Dashboard) approveDevice(w http.ResponseWriter, r *http.Request, machine string) {
	code := r.FormValue("code")
	who := remote.Session{Account: d.account, Machine: machine}.Who()
	if !d.guesses.allowed(who) {
		w.WriteHeader(http.StatusTooManyRequests)
		d.devicePage(w, r, machine, nil, "", "Too many codes that no machine was waiting with. Try again in ten minutes.")
		return
	}
	dev, ok := d.devices.Find(code)
	if !ok {
		d.guesses.miss(who)
		w.WriteHeader(http.StatusNotFound)
		d.devicePage(w, r, machine, nil, "", "No machine is waiting with that code. A code lasts ten minutes: run wong setup again for a new one.")
		return
	}
	switch r.FormValue("do") {
	case "deny":
		d.devices.Deny(code)
		d.devicePage(w, r, machine, nil, "Turned away. The machine stops waiting.", "")
		return
	case "approve":
		dev.Name = remote.MachineName(r.FormValue("name"))
		taken, err := d.tokenNamed(dev.Name)
		if err != nil {
			d.fail(w, err)
			return
		}
		if taken && r.FormValue("replace") != "1" {
			d.devicePage(w, r, machine, &dev, "", "")
			return
		}
		if !d.devices.Approve(code, d.store, dev.Name) {
			w.WriteHeader(http.StatusNotFound)
			d.devicePage(w, r, machine, nil, "", "That machine stopped waiting. Run wong setup on it again.")
			return
		}
		d.log.Info("device approved", "name", dev.Name, "from", dev.From, "by", machine)
		d.devicePage(w, r, machine, nil, "Connected. "+dev.Name+" carries on by itself in its terminal; you can close this tab.", "")
		return
	}
	d.devicePage(w, r, machine, &dev, "", "")
}

func (d *Dashboard) devicePage(w http.ResponseWriter, r *http.Request, machine string, dev *remote.Device, notice, problem string) {
	b, err := loadBrain(d.store)
	if err != nil {
		d.fail(w, err)
		return
	}
	p, err := d.base(r, "device", machine, b)
	if err != nil {
		d.fail(w, err)
		return
	}
	p.Title, p.Device, p.Notice, p.Error = "Connect a machine", dev, notice, problem
	if dev != nil {
		if p.Taken, err = d.tokenNamed(dev.Name); err != nil {
			d.fail(w, err)
			return
		}
	}
	d.render(w, "device", p)
}

func (d *Dashboard) tokenNamed(name string) (bool, error) {
	tokens, err := d.store.Tokens()
	for _, t := range tokens {
		if t.Name == name {
			return true, err
		}
	}
	return false, err
}
