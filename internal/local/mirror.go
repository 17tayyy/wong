// Package local serves a sealed brain on the machine that holds its key: a wong server of the decrypted copy, on
// localhost only, whose dashboard and MCP read and write that copy while it syncs with the brain's folder, and the
// folder syncs encrypted with the brain's server.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/dashboard"
	"github.com/17tayyy/wong/internal/mcp"
	"github.com/17tayyy/wong/internal/remote"
)

const (
	dbPath     = ".wong/local.db"
	statePath  = ".wong/local-sync.json"
	lockPath   = ".wong/local.json"
	cloudEvery = 30 * time.Second
)

// Mirror is the server of a sealed brain's decrypted copy on this machine.
type Mirror struct {
	URL   string
	Token string
	brain string
	cloud *remote.Client
	local *remote.Client
	store *remote.Store
	srv   *http.Server
	last  time.Time
	log   *slog.Logger
}

type lock struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Find returns the address and token of the copy another wong process serves for this brain, when it answers.
func Find(brain string) (url, token string, ok bool) {
	data, err := os.ReadFile(filepath.Join(brain, lockPath))
	if err != nil {
		return "", "", false
	}
	var l lock
	if json.Unmarshal(data, &l) != nil || l.URL == "" {
		return "", "", false
	}
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(l.URL + "/api/health")
	if err != nil {
		return "", "", false
	}
	resp.Body.Close()
	return l.URL, l.Token, resp.StatusCode == http.StatusOK
}

// Start opens the decrypted copy of the brain whose folder is brain, kept in sync with the server through cloud, and
// serves it on a free port of localhost.
func Start(brain string, cloud *remote.Client, version string, log *slog.Logger) (*Mirror, error) {
	if err := os.MkdirAll(filepath.Join(brain, ".wong"), 0o700); err != nil {
		return nil, err
	}
	if err := ignoreCopy(brain); err != nil {
		return nil, err
	}
	store, err := remote.Open(filepath.Join(brain, dbPath))
	if err != nil {
		return nil, err
	}
	name, _ := os.Hostname()
	if name == "" {
		name = "this machine"
	}
	token, err := store.NewToken(name)
	if err != nil {
		store.Close()
		return nil, err
	}
	signins := remote.NewSignIns()
	web, err := dashboard.Handler(store, log, dashboard.Options{Devices: remote.NewDevices(), SignIns: signins})
	if err != nil {
		store.Close()
		return nil, err
	}
	api := remote.Handler(store, log, remote.Webhooks{})
	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/api/signin", remote.SignInHandler(store, signins, log))
	root.Handle("/mcp", mcp.Handler(store, log, version))
	root.Handle("/", web)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		store.Close()
		return nil, err
	}
	m := &Mirror{URL: "http://" + ln.Addr().String(), Token: token, brain: brain, cloud: cloud, store: store, log: log,
		srv: &http.Server{Handler: root, ReadHeaderTimeout: 10 * time.Second}}
	m.local = remote.NewClient(m.URL, token, 30*time.Second)
	m.local.State = statePath
	go func() {
		if err := m.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("local server", "err", err)
		}
	}()
	data, err := json.Marshal(lock{URL: m.URL, Token: token})
	if err == nil {
		err = os.WriteFile(filepath.Join(brain, lockPath), data, 0o600)
	}
	if err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// Sync brings the server's changes into the folder now and then, the folder's into the copy and the copy's edits into
// the folder, and sends the folder's changes to the server, encrypted.
func (m *Mirror) Sync(ctx context.Context) error {
	toCloud := false
	if time.Since(m.last) >= cloudEvery {
		if _, err := remote.Pull(ctx, m.cloud, m.brain); err != nil {
			return err
		}
		m.last, toCloud = time.Now(), true
	}
	if _, err := remote.Push(ctx, m.local, m.brain); err != nil {
		return err
	}
	res, err := remote.Pull(ctx, m.local, m.brain)
	if err != nil {
		return err
	}
	if toCloud || len(res.Updated)+len(res.Deleted) > 0 {
		_, err = remote.Push(ctx, m.cloud, m.brain)
	}
	return err
}

// Run syncs every interval until ctx ends, then syncs once more and stops serving.
func (m *Mirror) Run(ctx context.Context, interval time.Duration) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := m.Sync(ctx); err != nil && ctx.Err() == nil {
			m.log.Warn("local sync", "err", err)
		}
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			m.last = time.Time{}
			err := m.Sync(final)
			return errors.Join(err, m.Close())
		case <-tick.C:
		}
	}
}

// ignoreCopy keeps the copy, its sync state and its token out of the brain's own git, which commits the rest.
func ignoreCopy(brain string) error {
	path := filepath.Join(brain, ".wong", ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "local*" {
			return nil
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return os.WriteFile(path, append(data, "local*\n"...), 0o644)
}

// Close stops serving the copy, which stays on disk for the next time.
func (m *Mirror) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := m.srv.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = m.srv.Close()
	}
	if rmErr := os.Remove(filepath.Join(m.brain, lockPath)); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		err = errors.Join(err, rmErr)
	}
	return errors.Join(err, m.store.Close())
}
