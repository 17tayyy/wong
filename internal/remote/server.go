package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type historyResponse struct {
	Entries []Entry `json:"entries"`
}

type historyRequest struct {
	Entries []Entry `json:"entries"`
	Done    bool    `json:"done"`
}

type pushResponse struct {
	Accepted  []string `json:"accepted"`
	Conflicts []Change `json:"conflicts"`
}

type tasksResponse struct {
	Tasks []Task `json:"tasks"`
}

type eventsResponse struct {
	Cursor int64   `json:"cursor"`
	Events []Event `json:"events"`
}

type findingsRequest struct {
	Project  string          `json:"project"`
	Findings []check.Finding `json:"findings"`
}

type sealStart struct {
	Public          []byte `json:"public"`
	Wrapped         []byte `json:"wrapped"`
	RecoveryPublic  []byte `json:"recovery_public"`
	RecoveryWrapped []byte `json:"recovery_wrapped"`
	Cursor          int64  `json:"cursor"`
}

type keyApproval struct {
	Device  string `json:"device,omitempty"`
	Public  []byte `json:"public"`
	Wrapped []byte `json:"wrapped,omitempty"`
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
			if err := s.Store.Usable(); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			if r.Header.Get(SealHeader) == "" {
				sealed, err := s.Store.Sealed()
				if err != nil {
					log.Error("sealed", "err", err)
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				if sealed {
					http.Error(w, ErrOldClient.Error(), http.StatusUpgradeRequired)
					return
				}
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
			if errors.Is(err, ErrMustSeal) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if errors.Is(err, ErrReadOnly) || errors.Is(err, ErrSuspended) {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			log.Error("push", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("push", "machine", s.Who(), "accepted", len(accepted), "conflicts", len(conflicts))
		writeJSON(w, pushResponse{Accepted: accepted, Conflicts: conflicts})
	}))
	mux.HandleFunc("GET /api/standing", metered(func(w http.ResponseWriter, _ *http.Request, s Session) {
		st, err := s.Store.Standing()
		if err != nil {
			log.Error("standing", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		st.Size = 0
		if st.Moved, err = s.Store.MovedTo(); err != nil {
			log.Error("standing", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, st)
	}))
	mux.HandleFunc("GET /api/history", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		entries, err := s.Store.ExportHistory(after, 200)
		if err != nil {
			log.Error("history", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, historyResponse{Entries: entries})
	}))
	mux.HandleFunc("POST /api/history", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		var req historyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		switch err := s.Store.ImportHistory(req.Entries, req.Done); {
		case errors.Is(err, ErrNotMoving), errors.Is(err, ErrMustSeal):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, ErrBadPush):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, ErrLimit):
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		case errors.Is(err, ErrReadOnly), errors.Is(err, ErrSuspended):
			http.Error(w, err.Error(), http.StatusForbidden)
		case err != nil:
			log.Error("history import", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			log.Info("history import", "machine", s.Who(), "entries", len(req.Entries), "done", req.Done)
			writeJSON(w, struct{}{})
		}
	}))
	mux.HandleFunc("POST /api/moved", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := s.Store.SetMovedTo(req.URL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Info("brain moved", "machine", s.Who(), "to", req.URL)
		writeJSON(w, struct{}{})
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
	mux.HandleFunc("GET /api/events", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		if err != nil && r.URL.Query().Get("since") != "" {
			http.Error(w, "since must be a number", http.StatusBadRequest)
			return
		}
		events, cursor, err := s.Store.Events(since)
		if err != nil {
			log.Error("events", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, eventsResponse{Cursor: cursor, Events: events})
	}))
	mux.HandleFunc("GET /api/keys", metered(func(w http.ResponseWriter, r *http.Request, s Session) {
		keys, err := s.Store.Keys(s.Machine)
		if err != nil {
			log.Error("keys", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, keys)
	}))
	keyCall := func(do func(Session, keyApproval, sealStart) error) http.HandlerFunc {
		return metered(func(w http.ResponseWriter, r *http.Request, s Session) {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			var k keyApproval
			var start sealStart
			if err == nil {
				err = errors.Join(json.Unmarshal(body, &k), json.Unmarshal(body, &start))
			}
			if err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			switch err := do(s, k, start); {
			case errors.Is(err, ErrSealed), errors.Is(err, ErrBehind), errors.Is(err, ErrNoKey), errors.Is(err, ErrCloudAIsOff):
				http.Error(w, err.Error(), http.StatusConflict)
			case err != nil:
				log.Error("keys", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
			default:
				log.Info("keys", "machine", s.Who(), "call", r.URL.Path)
				writeJSON(w, struct{}{})
			}
		})
	}
	mux.HandleFunc("POST /api/keys/start", keyCall(func(s Session, _ keyApproval, k sealStart) error {
		return s.Store.StartSeal(s.Machine, k.Public, k.Wrapped, k.RecoveryPublic, k.RecoveryWrapped, k.Cursor)
	}))
	mux.HandleFunc("POST /api/keys/request", keyCall(func(s Session, k keyApproval, _ sealStart) error {
		return s.Store.RequestKey(s.Machine, k.Public)
	}))
	mux.HandleFunc("POST /api/keys/approve", keyCall(func(s Session, k keyApproval, _ sealStart) error {
		return s.Store.ApproveKey(s.Machine, k.Device, k.Public, k.Wrapped)
	}))
	mux.HandleFunc("POST /api/keys/own", keyCall(func(s Session, k keyApproval, _ sealStart) error {
		return s.Store.KeepOwnKey(s.Machine, k.Public, k.Wrapped)
	}))
	mux.HandleFunc("POST /api/keys/cloud-ais", keyCall(func(s Session, k keyApproval, _ sealStart) error {
		return s.Store.LetCloudAIs(s.Machine, k.Wrapped)
	}))
	mux.HandleFunc("DELETE /api/keys/cloud-ais", keyCall(func(s Session, _ keyApproval, _ sealStart) error {
		return s.Store.StopCloudAIs()
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
