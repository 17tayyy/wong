package remote

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

const maxBody = 64 << 20

type changesResponse struct {
	Cursor  int64    `json:"cursor"`
	Changes []Change `json:"changes"`
}

type pushRequest struct {
	Changes []Upload `json:"changes"`
}

type pushResponse struct {
	Accepted  []string `json:"accepted"`
	Conflicts []Change `json:"conflicts"`
}

type tasksResponse struct {
	Tasks []Task `json:"tasks"`
}

// Handler serves the brain's API behind a bearer token, and the webhooks whose secret is set.
func Handler(store *Store, log *slog.Logger, hooks Webhooks) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/changes", authorized(store, func(w http.ResponseWriter, r *http.Request, _ string) {
		since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if err != nil && r.URL.Query().Get("since") != "" {
			http.Error(w, "since must be a number", http.StatusBadRequest)
			return
		}
		changes, cursor, err := store.Changes(since)
		if err != nil {
			log.Error("changes", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, changesResponse{Cursor: cursor, Changes: changes})
	}))
	mux.HandleFunc("POST /api/push", authorized(store, func(w http.ResponseWriter, r *http.Request, machine string) {
		var req pushRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		accepted, conflicts, err := store.Apply(machine, req.Changes)
		if err != nil {
			if errors.Is(err, ErrBadPush) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			log.Error("push", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("push", "machine", machine, "accepted", len(accepted), "conflicts", len(conflicts))
		writeJSON(w, pushResponse{Accepted: accepted, Conflicts: conflicts})
	}))
	mux.HandleFunc("GET /api/tasks", authorized(store, func(w http.ResponseWriter, r *http.Request, _ string) {
		tasks, err := store.Tasks(r.URL.Query().Get("project"))
		if err != nil {
			log.Error("tasks", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, tasksResponse{Tasks: tasks})
	}))
	if hooks.GitHubSecret != "" {
		mux.HandleFunc("POST /hooks/github", githubHook(store, log, hooks.GitHubSecret))
	}
	if hooks.LinearSecret != "" {
		mux.HandleFunc("POST /hooks/linear", linearHook(store, log, hooks.LinearSecret))
	}
	return mux
}

func authorized(store *Store, next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		machine, err := store.Machine(token)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if machine == "" {
			http.Error(w, "unknown token", http.StatusUnauthorized)
			return
		}
		next(w, r, machine)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
