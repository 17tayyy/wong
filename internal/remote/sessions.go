package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	SessionTTL = 30 * 24 * time.Hour
	signInTTL  = 2 * time.Minute
	maxSignIns = 1024
)

// NewSession opens a dashboard session on this brain for someone who showed a token or a sign-in link; via says which.
func (s *Store) NewSession(via string) (string, error) {
	session, err := s.newSecret()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now.Format(time.RFC3339)); err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO sessions (hash, via, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		Sha([]byte(session)), via, now.Format(time.RFC3339), now.Add(SessionTTL).Format(time.RFC3339))
	return session, err
}

func (s *Store) Visit(session string) (Session, error) {
	var via string
	err := s.db.QueryRow(`SELECT via FROM sessions WHERE hash = ? AND expires_at > ?`,
		Sha([]byte(session)), time.Now().UTC().Format(time.RFC3339)).Scan(&via)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, err
	}
	return Session{Store: s, Account: s.id, Machine: via}, nil
}

// EndSession signs a dashboard session out.
func (s *Store) EndSession(session string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE hash = ?`, Sha([]byte(session)))
	return err
}

// SignIns are links that open a dashboard session once, within two minutes of being made.
type SignIns struct {
	mu    sync.Mutex
	links map[string]SignIn
	now   func() time.Time
}

// SignIn is what a sign-in link opens: a brain, who made the link, and the page of the dashboard to land on.
type SignIn struct {
	Store *Store
	Via   string
	Next  string
	made  time.Time
}

func NewSignIns() *SignIns {
	return &SignIns{links: map[string]SignIn{}, now: time.Now}
}

// Issue makes the code of a link that opens a session on a brain and lands on next, a page of the dashboard.
func (l *SignIns) Issue(store *Store, via, next string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	if len(l.links) >= maxSignIns {
		return "", errors.New("too many sign-in links are waiting")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw)
	l.links[code] = SignIn{Store: store, Via: via, Next: next, made: l.now()}
	return code, nil
}

// Redeem spends a link's code and returns what it opens.
func (l *SignIns) Redeem(code string) (SignIn, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	for c, link := range l.links {
		if subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			delete(l.links, c)
			return link, true
		}
	}
	return SignIn{}, false
}

func (l *SignIns) prune() {
	for code, link := range l.links {
		if l.now().Sub(link.made) > signInTTL {
			delete(l.links, code)
		}
	}
}

// SignInLink is a link that opens the dashboard signed in, once.
type SignInLink struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
}

func (l *SignIns) link(r *http.Request, store *Store, via, next string) (SignInLink, error) {
	code, err := l.Issue(store, via, next)
	return SignInLink{URL: baseURL(r) + "/signin/" + code, ExpiresIn: int(signInTTL.Seconds())}, err
}

// SignInHandler lets a machine with a token make a link that opens the dashboard on its brain, for wong open.
func SignInHandler(brains Brains, signins *SignIns, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/signin", authorized(brains, func(w http.ResponseWriter, r *http.Request, s Session) {
		link, err := signins.link(r, s.Store, s.Machine, "/")
		if err != nil {
			log.Error("signin", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("signin link", "machine", s.Who())
		writeJSON(w, link)
	}))
	return mux
}

type adminAccount struct {
	Account string `json:"account"`
	Created bool   `json:"created"`
}

// AdminHandler lets a sign-up service, such as wong cloud's, make accounts, sign people in to them, on the page of
// the dashboard given as next, and see how they are.
// It holds the admin token; with none, it answers nothing.
func AdminHandler(folder *Folder, signins *SignIns, adminToken string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	admitted := func(r *http.Request) bool {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return adminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(adminToken)) == 1
	}
	admin := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !admitted(r) {
				http.NotFound(w, r)
				return
			}
			if ValidAccount(r.PathValue("account")) != nil {
				http.Error(w, "invalid account", http.StatusBadRequest)
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("PUT /admin/accounts/{account}", admin(func(w http.ResponseWriter, r *http.Request) {
		account := r.PathValue("account")
		_, created, err := folder.Create(account)
		if err != nil {
			log.Error("admin", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if created {
			log.Info("account created", "account", account)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(adminAccount{Account: account, Created: true})
			return
		}
		writeJSON(w, adminAccount{Account: account})
	}))
	mux.HandleFunc("PUT /admin/accounts/{account}/standing", admin(func(w http.ResponseWriter, r *http.Request) {
		var st Standing
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&st); err != nil {
			http.Error(w, "invalid standing", http.StatusBadRequest)
			return
		}
		store, err := folder.Store(r.PathValue("account"))
		if errors.Is(err, ErrNoAccount) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err == nil {
			err = store.SetStanding(st)
		}
		if err != nil {
			log.Error("admin", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("account standing", "account", r.PathValue("account"), "mode", st.Mode, "size", st.Size)
		writeJSON(w, st)
	}))
	mux.HandleFunc("DELETE /admin/accounts/{account}", admin(func(w http.ResponseWriter, r *http.Request) {
		err := folder.Delete(r.PathValue("account"))
		if errors.Is(err, ErrNoAccount) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			log.Error("admin", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("account deleted", "account", r.PathValue("account"))
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		if !admitted(r) {
			http.NotFound(w, r)
			return
		}
		accounts, err := folder.Accounts()
		if err != nil {
			log.Error("admin", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, struct {
			Accounts []AccountInfo `json:"accounts"`
		}{accounts})
	})
	mux.HandleFunc("POST /admin/accounts/{account}/signin", admin(func(w http.ResponseWriter, r *http.Request) {
		store, err := folder.Store(r.PathValue("account"))
		if errors.Is(err, ErrNoAccount) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		var link SignInLink
		if err == nil {
			link, err = signins.link(r, store, "sign-in", r.FormValue("next"))
		}
		if err != nil {
			log.Error("admin", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, link)
	}))
	return mux
}

// BaseURL is the scheme and host a request reached, for links back to this server.
func BaseURL(r *http.Request) string { return baseURL(r) }

func baseURL(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}
