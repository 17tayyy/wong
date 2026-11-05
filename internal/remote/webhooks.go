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
	"strconv"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/linear"
)

const (
	linearTolerance = time.Minute
	maxHookBody     = 25 << 20
)

// Webhooks holds the secrets that sign GitHub's and Linear's webhooks for a single brain, before the ones it keeps
// itself, and the GitHub App whose webhooks reach the brains linked to its installations.
type Webhooks struct {
	GitHubSecret string
	LinearSecret string
	App          *GitHubApp
	Links        *Links
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
	if env.App != nil && env.Links != nil {
		mux.HandleFunc("POST /hooks/github-app", appHook(brains, env, log))
	}
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
		tasks, why, err := takePush(store, push, log)
		if err != nil {
			log.Error("github", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if why != "" {
			ignore(w, log, "github", why)
			return
		}
		writeJSON(w, hookResponse{Tasks: tasks})
	}
}

// takePush turns a push to a repo's default branch, or a new v* tag, into tasks for the notes of the repo's project
// that it leaves something to fix in; a sealed brain keeps it for its machines instead. why says why a push is ignored.
func takePush(store *Store, push githubPush, log *slog.Logger) (tasks int, why string, err error) {
	tag, isTag := strings.CutPrefix(push.Ref, "refs/tags/")
	switch {
	case isTag && (!versionTagRE.MatchString(tag) || !push.Created || push.Deleted):
		return 0, "tag " + tag + " of " + push.Repository.FullName + " is not a new v* tag", nil
	case !isTag && push.Ref != "refs/heads/"+push.Repository.DefaultBranch:
		return 0, "not the default branch of " + push.Repository.FullName, nil
	}
	sealed, err := store.Sealed()
	if err != nil {
		return 0, "", err
	}
	if sealed {
		if err := keepPush(store, push, tag, isTag); err != nil {
			return 0, "", err
		}
		log.Info("github kept for the machines", "repo", push.Repository.FullName)
		return 0, "", nil
	}
	remotes, err := store.remotes()
	if err != nil {
		return 0, "", err
	}
	remote, ok := remotes.Lookup(push.Repository.FullName)
	if !ok {
		return 0, push.Repository.FullName + " is in no project", nil
	}
	notes, err := store.notes(remote.Project)
	if err != nil {
		return 0, "", err
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
		return 0, "", err
	}
	if isTag {
		log.Info("github release", "repo", push.Repository.FullName, "tag", tag, "tasks", pending)
		return pending, "", nil
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
			return 0, "", err
		}
		total += n
	}
	log.Info("github push", "repo", push.Repository.FullName, "removed", len(removed), "tasks", total)
	return total, "", nil
}

type githubPull struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// appHook takes the GitHub App's webhooks, for every account linked to the installation they come from: pushes,
// which leave tasks as a repo's own webhook does, and pull requests, which get wong's check.
func appHook(brains Brains, hooks Webhooks, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := signedBody(w, r, log, hooks.App.WebhookSecret, "X-Hub-Signature-256", "sha256=")
		if !ok {
			return
		}
		var head struct {
			Action       string `json:"action"`
			Installation struct {
				ID int64 `json:"id"`
			} `json:"installation"`
		}
		if err := json.Unmarshal(body, &head); err != nil {
			http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := strconv.FormatInt(head.Installation.ID, 10)
		kind := r.Header.Get("X-GitHub-Event")
		switch {
		case kind == "installation" && head.Action == "deleted":
			if err := hooks.Links.Forget("github", id); err != nil {
				log.Error("github app", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			ignore(w, log, "github app", "installation "+id+" deleted")
			return
		case kind == "push":
		case kind == "pull_request" && slices.Contains([]string{"opened", "reopened", "synchronize"}, head.Action):
		default:
			ignore(w, log, "github app", strings.TrimSpace("event "+kind+" "+head.Action))
			return
		}
		accounts := hooks.Links.Accounts("github", id)
		if len(accounts) == 0 {
			ignore(w, log, "github app", "installation "+id+" is linked to no brain")
			return
		}
		var push githubPush
		var pull githubPull
		var err error
		if kind == "push" {
			err = json.Unmarshal(body, &push)
		} else {
			err = json.Unmarshal(body, &pull)
		}
		if err != nil {
			http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		total, failed := 0, false
		for _, account := range accounts {
			store, err := storeOf(brains, account)
			if err == nil && kind == "push" {
				var n int
				n, _, err = takePush(store, push, log)
				total += n
			} else if err == nil {
				err = takePR(r.Context(), hooks.App, store, head.Installation.ID, pull.Repository.FullName, pull.Number, pull.PullRequest.Head.SHA)
				if err == nil {
					log.Info("github pull request", "repo", pull.Repository.FullName, "number", pull.Number)
				}
			}
			if err != nil {
				log.Error("github app", "account", account, "err", err)
				failed = true
			}
		}
		if failed {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, hookResponse{Tasks: total})
	}
}

// storeOf returns the brain of an account: the server's only one when it keeps a single brain.
func storeOf(brains Brains, account string) (*Store, error) {
	switch b := brains.(type) {
	case *Store:
		return b, nil
	case *Folder:
		return b.Store(account)
	}
	return nil, errors.New("this server keeps no brains")
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
