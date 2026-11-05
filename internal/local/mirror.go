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
	"time"

	brainfs "github.com/17tayyy/wong/internal/brain"
	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/dashboard"
	"github.com/17tayyy/wong/internal/mcp"
	"github.com/17tayyy/wong/internal/remote"
)

const (
	dbPath     = ".wong/local.db"
	statePath  = ".wong/local-sync.json"
	lockPath   = ".wong/local.json"
	cloudEvery = 30 * time.Second
	checkEvery = 5 * time.Minute
)

// Mirror is the server of a sealed brain's decrypted copy on this machine. A sealed brain's server cannot tell which
// notes are outdated, so the mirror shows what this machine finds: the tasks it keeps from the webhooks' events, and,
// when Checks names the brain, what wong check finds every few minutes.
type Mirror struct {
	URL     string
	Token   string
	Checks  check.Options
	brain   string
	machine string
	cloud   *remote.Client
	local   *remote.Client
	store   *remote.Store
	srv     *http.Server
	last    time.Time
	checked time.Time
	log     *slog.Logger
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
	if err := brainfs.IgnoreState(brain, "local*"); err != nil {
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
	web, err := dashboard.Handler(store, log, dashboard.Options{Devices: remote.NewDevices(), SignIns: signins, Keyring: remote.Holder{C: cloud}})
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
	m := &Mirror{URL: "http://" + ln.Addr().String(), Token: token, brain: brain, machine: name, cloud: cloud, store: store, log: log,
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

// Sync brings the server's changes into the folder now and then, with the webhooks' events, which it applies, the
// folder's into the copy and the copy's edits into the folder, and sends the folder's changes to the server, encrypted.
func (m *Mirror) Sync(ctx context.Context) error {
	toCloud := false
	if time.Since(m.last) >= cloudEvery {
		if _, err := remote.Pull(ctx, m.cloud, m.brain); err != nil {
			return err
		}
		m.last, toCloud = time.Now(), true
		if _, err := remote.OpenTasks(ctx, m.cloud, m.brain, ""); err != nil {
			m.log.Warn("local tasks", "err", err)
		}
		if err := remote.MirrorTasks(m.store, m.brain); err != nil {
			return err
		}
		if m.Checks.Brain != "" && time.Since(m.checked) >= checkEvery {
			m.checked = time.Now()
			if err := m.check(ctx); err != nil {
				m.log.Warn("local check", "err", err)
			}
		}
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

// check runs wong check on the brain and keeps what it finds, for each shelf, as this machine's findings.
func (m *Mirror) check(ctx context.Context) error {
	rep, err := check.Run(ctx, m.Checks)
	if err != nil {
		return err
	}
	byShelf := map[string][]check.Finding{"me": nil}
	dirs, err := os.ReadDir(filepath.Join(m.brain, "projects"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, d := range dirs {
		if d.IsDir() {
			byShelf[d.Name()] = nil
		}
	}
	for _, f := range rep.Findings {
		if shelf, ok := remote.NoteProject(f.Note); ok {
			byShelf[shelf] = append(byShelf[shelf], f)
		}
	}
	var errs []error
	for shelf, findings := range byShelf {
		errs = append(errs, m.store.ReportFindings(m.machine, shelf, findings))
	}
	return errors.Join(errs...)
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
