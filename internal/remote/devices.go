package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	deviceTTL     = 10 * time.Minute
	maxDevices    = 64
	maxPerAddress = 4
	codeAlphabet  = "BCDFGHJKLMNPQRSTVWXZ"
	codeLength    = 8
	defaultPollIn = 5 * time.Second
)

// ErrTooManyDevices marks a request to connect refused because too many are waiting.
var ErrTooManyDevices = errors.New("too many machines are waiting to be approved")

// Devices holds the machines that asked to connect until someone signed in approves them, for ten minutes.
type Devices struct {
	PollEvery time.Duration
	mu        sync.Mutex
	waiting   map[string]*Device
	now       func() time.Time
}

// Device is a machine waiting to connect: the code it shows, the name it asked for, and where it asked from.
type Device struct {
	Code   string
	Name   string
	From   string
	Asked  time.Time
	secret string
	store  *Store
	denied bool
}

func NewDevices() *Devices {
	return &Devices{PollEvery: defaultPollIn, waiting: map[string]*Device{}, now: time.Now}
}

// Ask records a machine that wants to connect and returns the secret it polls with and the code a person approves.
func (d *Devices) Ask(name, from string) (secret, code string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	same := 0
	for _, dev := range d.waiting {
		if dev.From == from {
			same++
		}
	}
	if len(d.waiting) >= maxDevices || same >= maxPerAddress {
		return "", "", ErrTooManyDevices
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	for code == "" || d.waiting[code] != nil {
		pick := make([]byte, codeLength)
		if _, err := rand.Read(pick); err != nil {
			return "", "", err
		}
		for i, b := range pick {
			pick[i] = codeAlphabet[int(b)%len(codeAlphabet)]
		}
		code = string(pick)
	}
	secret = hex.EncodeToString(raw)
	d.waiting[code] = &Device{Code: code, Name: name, From: from, Asked: d.now(), secret: secret}
	return secret, code, nil
}

func (d *Device) waitingFor() bool { return d.store == nil && !d.denied }

// Find returns the machine waiting with a code, typed in any case and with or without its dash.
func (d *Devices) Find(code string) (Device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	dev, ok := d.waiting[NormalCode(code)]
	if !ok || !dev.waitingFor() {
		return Device{}, false
	}
	return *dev, true
}

// Approve lets the machine with a code into a brain under a name; its token is made when it comes for it.
func (d *Devices) Approve(code string, store *Store, name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	dev, ok := d.waiting[NormalCode(code)]
	if !ok || !dev.waitingFor() {
		return false
	}
	dev.store, dev.Name = store, name
	return true
}

// Deny turns away the machine with a code.
func (d *Devices) Deny(code string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	dev, ok := d.waiting[NormalCode(code)]
	if !ok || !dev.waitingFor() {
		return false
	}
	dev.denied = true
	return true
}

// DeviceStatus is where a machine's request to connect stands.
type DeviceStatus int

const (
	DeviceWaiting DeviceStatus = iota
	DeviceApproved
	DeviceDenied
	DeviceGone
)

// Poll answers a waiting machine: once approved, it makes its token, which it hands out once with the name it got.
func (d *Devices) Poll(secret string) (DeviceStatus, Session, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	for code, dev := range d.waiting {
		if subtle.ConstantTimeCompare([]byte(dev.secret), []byte(secret)) != 1 {
			continue
		}
		switch {
		case dev.denied:
			delete(d.waiting, code)
			return DeviceDenied, Session{}, "", nil
		case dev.store == nil:
			return DeviceWaiting, Session{}, "", nil
		}
		delete(d.waiting, code)
		token, err := dev.store.NewToken(dev.Name)
		return DeviceApproved, Session{Store: dev.store, Account: dev.store.id, Machine: dev.Name}, token, err
	}
	return DeviceGone, Session{}, "", nil
}

func (d *Devices) prune() {
	for code, dev := range d.waiting {
		if d.now().Sub(dev.Asked) > deviceTTL {
			delete(d.waiting, code)
		}
	}
}

// NormalCode turns a code as a person types it into the one a machine shows: upper case, letters only.
func NormalCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		if r < 'A' || r > 'Z' {
			return -1
		}
		return r
	}, code)
}

// ShowCode writes a code as machines show it, in two halves.
func ShowCode(code string) string {
	if len(code) != codeLength {
		return code
	}
	return code[:codeLength/2] + "-" + code[codeLength/2:]
}

type deviceAsk struct {
	Name string `json:"name"`
}

// DeviceStart is what a machine gets when it asks to connect: the code to show, where to enter it, and how often to poll.
type DeviceStart struct {
	Secret    string `json:"device_code"`
	Code      string `json:"user_code"`
	URL       string `json:"verification_uri"`
	ExpiresIn int    `json:"expires_in"`
	Interval  int    `json:"interval"`
}

type devicePoll struct {
	Secret string `json:"device_code"`
}

// DeviceToken is what an approved machine gets: its token, the name it was given and the account it joined.
type DeviceToken struct {
	Token   string `json:"token"`
	Name    string `json:"name"`
	Account string `json:"account,omitempty"`
}

// DeviceHandler lets a machine without a token ask to connect, and come back for its token once someone approves it.
func DeviceHandler(devices *Devices, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/device", func(w http.ResponseWriter, r *http.Request) {
		var req deviceAsk
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		from := ClientIP(r)
		secret, code, err := devices.Ask(MachineName(req.Name), from)
		if errors.Is(err, ErrTooManyDevices) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			log.Error("device", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("device asked", "name", MachineName(req.Name), "from", from)
		writeJSON(w, DeviceStart{Secret: secret, Code: ShowCode(code), URL: baseURL(r) + "/device",
			ExpiresIn: int(deviceTTL.Seconds()), Interval: max(1, int(devices.PollEvery.Seconds()))})
	})
	mux.HandleFunc("POST /api/device/token", func(w http.ResponseWriter, r *http.Request) {
		var req devicePoll
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Secret == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		status, s, token, err := devices.Poll(req.Secret)
		switch {
		case err != nil:
			log.Error("device", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		case status == DeviceWaiting:
			w.WriteHeader(http.StatusAccepted)
		case status == DeviceDenied:
			http.Error(w, "the machine was turned away", http.StatusForbidden)
		case status == DeviceGone:
			http.Error(w, "the code expired", http.StatusGone)
		default:
			log.Info("device connected", "machine", s.Who())
			writeJSON(w, DeviceToken{Token: token, Name: s.Machine, Account: s.Account})
		}
	})
	return mux
}

// ClientIP is the address a request comes from. Behind a proxy on this machine or network, such as a Cloudflare
// tunnel, it is the visitor's as the proxy passes it: Cloudflare's header, or the address the proxy added last.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err != nil || !(addr.IsLoopback() || addr.IsPrivate()) {
		return host
	}
	if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip"))); err == nil {
		return ip.String()
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if ip, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
		return ip.String()
	}
	return host
}

// MachineName turns a host name into a token name: lower case letters, digits and hyphens.
func MachineName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name, _, _ = strings.Cut(name, ".")
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, "-")
	if len(name) > 40 {
		name = name[:40]
	}
	if name == "" {
		return "machine"
	}
	return name
}
