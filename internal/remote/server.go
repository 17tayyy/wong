package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/17tayyy/wong/internal/check"
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

type findingsRequest struct {
	Project  string          `json:"project"`
	Findings []check.Finding `json:"findings"`
}

// Handler serves the API of the brain each bearer token opens, and the webhooks whose secret is set.
func Handler(brains Brains, log *slog.Logger, hooks Webhooks) http.Handler {
	mux := http.NewServeMux()
	paced := &pace{buckets: map[*Store]*allowance{}}
	metered := func(next func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
		return authorized(brains, func(w http.ResponseWriter, r *http.Request, s Session) {
			if !paced.allow(s.Store) {
				w.Header().Set("Retry-After", "10")
				http.Error(w, fmt.Sprintf("too many requests: this brain takes %d a minute", s.Store.limits.Requests), http.StatusTooManyRequests)
				return
			}
			next(w, r, s)
		})
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/changes", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if err != nil && r.URL.Query().Get("since") != "" {
			http.Error(w, "since must be a number", http.StatusBadRequest)
			return
		}
		changes, cursor, err := s.Store.Changes(since)
		if err != nil {
			log.Error("changes", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, changesResponse{Cursor: cursor, Changes: changes})
	}))
	mux.HandleFunc("POST /api/push", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		var req pushRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		accepted, conflicts, err := s.Store.Apply(s.Machine, req.Changes)
		if err != nil {
			if errors.Is(err, ErrBadPush) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if errors.Is(err, ErrLimit) {
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
				return
			}
			log.Error("push", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("push", "machine", s.Who(), "accepted", len(accepted), "conflicts", len(conflicts))
		writeJSON(w, pushResponse{Accepted: accepted, Conflicts: conflicts})
	}))
	mux.HandleFunc("GET /api/tasks", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		tasks, err := s.Store.Tasks(r.URL.Query().Get("project"))
		if err != nil {
			log.Error("tasks", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, tasksResponse{Tasks: tasks})
	}))
	mux.HandleFunc("POST /api/findings", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		var req findingsRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil || req.Project == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := s.Store.ReportFindings(s.Machine, req.Project, req.Findings); err != nil {
			log.Error("findings", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, struct{}{})
	}))
	hookBrains(mux, brains, log, hooks)
	return mux
}

func authorized(brains Brains, next func(http.ResponseWriter, *http.Request, Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		s, err := brains.Auth(token)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if s.Store == nil {
			http.Error(w, "unknown token", http.StatusUnauthorized)
			return
		}
		next(w, r, s)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
