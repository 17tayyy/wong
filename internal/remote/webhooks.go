package remote

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/linear"
)

const (
	linearTolerance = time.Minute
	maxHookBody     = 25 << 20
)

// Webhooks holds the secrets that sign GitHub's and Linear's webhooks; an empty one leaves its endpoint off.
type Webhooks struct {
	GitHubSecret string
	LinearSecret string
}

type githubPush struct {
	Ref        string `json:"ref"`
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
func githubHook(store *Store, log *slog.Logger, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		if push.Ref != "refs/heads/"+push.Repository.DefaultBranch {
			ignore(w, log, "github", "not the default branch of "+push.Repository.FullName)
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
		removed := map[string]event{}
		for _, c := range push.Commits {
			for _, p := range c.Added {
				delete(removed, p)
			}
			for _, p := range c.Removed {
				removed[p] = event{Repo: remote.Dir, Path: p, Commit: c.ID[:min(len(c.ID), 7)], When: c.Timestamp}
			}
		}
		notes, err := store.notes(remote.Project)
		if err != nil {
			log.Error("github", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		total := 0
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
func linearHook(store *Store, log *slog.Logger, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		notes, err := store.notes("")
		var n int
		if err == nil {
			n, err = store.addTasks(event{Issue: &issue}, "linear "+issue.Identifier, notes)
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
