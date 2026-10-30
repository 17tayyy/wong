package remote

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/linear"
)

const (
	linearTolerance = time.Minute
	maxHookBody     = 25 << 20
)

// Webhooks holds the secrets that sign GitHub's and Linear's webhooks for a single brain, before the ones it keeps itself.
type Webhooks struct {
	GitHubSecret string
	LinearSecret string
}

// Hooks names the webhooks a brain can take.
var Hooks = []string{"github", "linear"}

// HookSecret returns the secret that signs a webhook of this brain, "" when it has none.
func (s *Store) HookSecret(hook string) (string, error) {
	var secret string
	err := s.db.QueryRow(`SELECT secret FROM hooks WHERE name = ?`, hook).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return secret, err
}

// SetHookSecret keeps the secret that signs a webhook of this brain; an empty one turns the webhook off.
func (s *Store) SetHookSecret(hook, secret string) error {
	if !slices.Contains(Hooks, hook) {
		return fmt.Errorf("no webhook named %q", hook)
	}
	if secret == "" {
		_, err := s.db.Exec(`DELETE FROM hooks WHERE name = ?`, hook)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO hooks (name, secret, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET secret = excluded.secret, updated_at = excluded.updated_at`,
		hook, secret, time.Now().UTC().Format(time.RFC3339))
	return err
}

// hookBrain finds the brain a webhook request is for and the secret that signs it; no store or secret means no such webhook.
type hookBrain func(r *http.Request, hook string) (*Store, string, error)

// hookBrains serves a single brain's webhooks at /hooks/<hook>, and each account's of a Folder at /hooks/<account>/<hook>.
func hookBrains(mux *http.ServeMux, brains Brains, log *slog.Logger, env Webhooks) {
	var find hookBrain
	prefix := "/hooks/"
	switch b := brains.(type) {
	case *Store:
		find = func(_ *http.Request, hook string) (*Store, string, error) {
			if secret := map[string]string{"github": env.GitHubSecret, "linear": env.LinearSecret}[hook]; secret != "" {
				return b, secret, nil
			}
			secret, err := b.HookSecret(hook)
			return b, secret, err
		}
	case *Folder:
		prefix = "/hooks/{account}/"
		find = func(r *http.Request, hook string) (*Store, string, error) {
			if ValidAccount(r.PathValue("account")) != nil {
				return nil, "", nil
			}
			s, err := b.Store(r.PathValue("account"))
			if errors.Is(err, ErrNoAccount) {
				return nil, "", nil
			}
			if err != nil {
				return nil, "", err
			}
			secret, err := s.HookSecret(hook)
			return s, secret, err
		}
	default:
		return
	}
	mux.HandleFunc("POST "+prefix+"github", githubHook(find, log))
	mux.HandleFunc("POST "+prefix+"linear", linearHook(find, log))
}

var versionTagRE = regexp.MustCompile(`^v\d`)

type githubPush struct {
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Created bool   `json:"created"`
	Deleted bool   `json:"deleted"`
	Pusher  struct {
		Name string `json:"name"`
	} `json:"pusher"`
	Repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Commits []struct {
		ID        string    `json:"id"`
		Timestamp time.Time `json:"timestamp"`
		Added     []string  `json:"added"`
		Removed   []string  `json:"removed"`
	} `json:"commits"`
}

type linearEvent struct {
	Action           string    `json:"action"`
	Type             string    `json:"type"`
	CreatedAt        time.Time `json:"createdAt"`
	WebhookTimestamp int64     `json:"webhookTimestamp"`
	Data             struct {
		Identifier  string     `json:"identifier"`
		CompletedAt *time.Time `json:"completedAt"`
		CanceledAt  *time.Time `json:"canceledAt"`
		State       struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"state"`
	} `json:"data"`
}

type hookResponse struct {
	Tasks   int    `json:"tasks"`
	Ignored string `json:"ignored,omitempty"`
}

// githubHook turns a push to a repo's default branch into tasks for the notes of its project that cite a removed file.
func githubHook(find hookBrain, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, secret, ok := hookTarget(w, r, find, log, "github")
		if !ok {
			return
		}
		body, ok := signedBody(w, r, log, secret, "X-Hub-Signature-256", "sha256=")
		if !ok {
			return
		}
		switch r.Header.Get("X-GitHub-Event") {
		case "push":
		case "ping":
			ignore(w, log, "github", "ping")
			return
		default:
			ignore(w, log, "github", "event "+r.Header.Get("X-GitHub-Event"))
			return
		}
		var push githubPush
		if err := json.Unmarshal(body, &push); err != nil {
			http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		tag, isTag := strings.CutPrefix(push.Ref, "refs/tags/")
		switch {
		case isTag && (!versionTagRE.MatchString(tag) || !push.Created || push.Deleted):
			ignore(w, log, "github", "tag "+tag+" of "+push.Repository.FullName+" is not a new v* tag")
			return
		case !isTag && push.Ref != "refs/heads/"+push.Repository.DefaultBranch:
			ignore(w, log, "github", "not the default branch of "+push.Repository.FullName)
			return
		}
		if sealed, err := store.Sealed(); err != nil || sealed {
			if err == nil {
				err = keepPush(store, push, tag, isTag)
			}
			if err != nil {
				log.Error("github", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			log.Info("github kept for the machines", "repo", push.Repository.FullName)
			writeJSON(w, hookResponse{})
			return
		}
		remotes, err := store.remotes()
		if err != nil {
			log.Error("github", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		remote, ok := remotes.Lookup(push.Repository.FullName)
		if !ok {
			ignore(w, log, "github", push.Repository.FullName+" is in no project")
			return
		}
		notes, err := store.notes(remote.Project)
		if err != nil {
			log.Error("github", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		deploy := event{Repo: remote.Dir, By: push.Pusher.Name, When: time.Now().UTC()}
		source := "github " + push.Repository.FullName + "@"
		if isTag {
			deploy.Tag, source = tag, source+tag
		} else {
			deploy.Branch, deploy.Commit = push.Repository.DefaultBranch, push.After[:min(len(push.After), 7)]
			source += deploy.Commit
		}
		pending, err := store.addTasks(deploy, source, notes)
		if err != nil {
			log.Error("github", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if isTag {
			log.Info("github release", "repo", push.Repository.FullName, "tag", tag, "tasks", pending)
			writeJSON(w, hookResponse{Tasks: pending})
			return
		}
		removed := map[string]event{}
		for _, c := range push.Commits {
			for _, p := range c.Added {
				delete(removed, p)
			}
			for _, p := range c.Removed {
				removed[p] = event{Repo: remote.Dir, Path: p, Commit: c.ID[:min(len(c.ID), 7)], When: c.Timestamp}
			}
		}
		total := pending
		for _, p := range slices.Sorted(maps.Keys(removed)) {
			ev := removed[p]
			n, err := store.addTasks(ev, "github "+push.Repository.FullName+"@"+ev.Commit, notes)
			if err != nil {
				log.Error("github", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			total += n
		}
		log.Info("github push", "repo", push.Repository.FullName, "removed", len(removed), "tasks", total)
		writeJSON(w, hookResponse{Tasks: total})
	}
}

// linearHook turns an issue that is now closed into tasks for the notes that cite it.
func linearHook(find hookBrain, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, secret, ok := hookTarget(w, r, find, log, "linear")
		if !ok {
			return
		}
		body, ok := signedBody(w, r, log, secret, "Linear-Signature", "")
		if !ok {
			return
		}
		var ev linearEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if sent := time.UnixMilli(ev.WebhookTimestamp); time.Since(sent).Abs() > linearTolerance {
			log.Warn("linear refused", "reason", "webhookTimestamp too far from now", "sent", sent)
			http.Error(w, "webhookTimestamp too far from now", http.StatusUnauthorized)
			return
		}
		if ev.Type != "Issue" || ev.Action == "remove" || ev.Data.Identifier == "" {
			ignore(w, log, "linear", strings.TrimSpace(ev.Type+" "+ev.Action))
			return
		}
		issue := linear.Issue{Identifier: ev.Data.Identifier, State: ev.Data.State.Name, Type: ev.Data.State.Type, ClosedAt: ev.CreatedAt}
		for _, at := range []*time.Time{ev.Data.CompletedAt, ev.Data.CanceledAt} {
			if at != nil {
				issue.ClosedAt = *at
			}
		}
		if !issue.Closed() {
			ignore(w, log, "linear", ev.Data.Identifier+" is not closed")
			return
		}
		sealed, err := store.Sealed()
		var n int
		switch {
		case err == nil && sealed:
			err = store.keepEvent(event{Issue: &issue}, "linear "+issue.Identifier, "")
		case err == nil:
			var notes []check.Note
			if notes, err = store.notes(""); err == nil {
				n, err = store.addTasks(event{Issue: &issue}, "linear "+issue.Identifier, notes)
			}
		}
		if err != nil {
			log.Error("linear", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("linear issue", "issue", issue.Identifier, "state", issue.State, "tasks", n)
		writeJSON(w, hookResponse{Tasks: n})
	}
}

// keepPush stores what a push to a sealed brain's repo reported, for its machines to apply: the tag or the commit on
// the default branch, and each file the push removed. Repo is left for the machine to fill with the repo's directory.
func keepPush(store *Store, push githubPush, tag string, isTag bool) error {
	full := push.Repository.FullName
	deploy := event{By: push.Pusher.Name, When: time.Now().UTC()}
	source := "github " + full + "@"
	if isTag {
		deploy.Tag, source = tag, source+tag
	} else {
		deploy.Branch, deploy.Commit = push.Repository.DefaultBranch, push.After[:min(len(push.After), 7)]
		source += deploy.Commit
	}
	if err := store.keepEvent(deploy, source, full); err != nil || isTag {
		return err
	}
	removed := map[string]event{}
	for _, c := range push.Commits {
		for _, p := range c.Added {
			delete(removed, p)
		}
		for _, p := range c.Removed {
			removed[p] = event{Path: p, Commit: c.ID[:min(len(c.ID), 7)], When: c.Timestamp}
		}
	}
	for _, p := range slices.Sorted(maps.Keys(removed)) {
		if err := store.keepEvent(removed[p], "github "+full+"@"+removed[p].Commit, full); err != nil {
			return err
		}
	}
	return nil
}

// hookTarget finds the brain and secret of a webhook request, answering it when there is no such webhook.
func hookTarget(w http.ResponseWriter, r *http.Request, find hookBrain, log *slog.Logger, hook string) (*Store, string, bool) {
	store, secret, err := find(r, hook)
	if err != nil {
		log.Error(hook, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, "", false
	}
	if store == nil || secret == "" {
		http.NotFound(w, r)
		return nil, "", false
	}
	return store, secret, true
}

// signedBody reads the request body and checks its hex HMAC-SHA256 against the secret, answering the request when it fails.
func signedBody(w http.ResponseWriter, r *http.Request, log *slog.Logger, secret, header, prefix string) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get(header), prefix))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		log.Warn("webhook refused", "path", r.URL.Path, "reason", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

func ignore(w http.ResponseWriter, log *slog.Logger, hook, reason string) {
	log.Info(hook+" ignored", "reason", reason)
	writeJSON(w, hookResponse{Ignored: reason})
}
