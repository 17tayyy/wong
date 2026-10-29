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
	links map[string]signIn
	now   func() time.Time
}

type signIn struct {
	store *Store
	via   string
	made  time.Time
}

func NewSignIns() *SignIns {
	return &SignIns{links: map[string]signIn{}, now: time.Now}
}

// Issue makes the code of a link that opens a session on a brain.
func (l *SignIns) Issue(store *Store, via string) (string, error) {
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
	l.links[code] = signIn{store: store, via: via, made: l.now()}
	return code, nil
}

// Redeem spends a link's code and returns the brain it opens and who made it.
func (l *SignIns) Redeem(code string) (*Store, string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	for c, link := range l.links {
		if subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			delete(l.links, c)
			return link.store, link.via, true
		}
	}
	return nil, "", false
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

func (l *SignIns) link(r *http.Request, store *Store, via string) (SignInLink, error) {
	code, err := l.Issue(store, via)
	return SignInLink{URL: baseURL(r) + "/signin/" + code, ExpiresIn: int(signInTTL.Seconds())}, err
}

// SignInHandler lets a machine with a token make a link that opens the dashboard on its brain, for wong open.
func SignInHandler(brains Brains, signins *SignIns, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/signin", authorized(brains, func(w http.ResponseWriter, r *http.Request, s Session) {
		link, err := signins.link(r, s.Store, s.Machine)
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

// AdminHandler lets a sign-up service, such as wong cloud's, make accounts and sign people in to them.
// It holds the admin token; with none, it answers nothing.
func AdminHandler(folder *Folder, signins *SignIns, adminToken string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	admin := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if adminToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(adminToken)) != 1 {
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
	mux.HandleFunc("POST /admin/accounts/{account}/signin", admin(func(w http.ResponseWriter, r *http.Request) {
		store, err := folder.Store(r.PathValue("account"))
		if errors.Is(err, ErrNoAccount) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		var link SignInLink
		if err == nil {
			link, err = signins.link(r, store, "sign-in")
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

// baseURL is the scheme and host a request reached, for links back to this server.
func baseURL(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}
