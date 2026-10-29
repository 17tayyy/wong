// Package dashboard serves the web view of the brain: the library, corrections, history and undo.
package dashboard

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/remote"
)

const cookieName = "wong_token"

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Dashboard serves the brains a server keeps; each request sees the one its token opens, as store.
type Dashboard struct {
	brains  remote.Brains
	store   *remote.Store
	account string
	log     *slog.Logger
	pages   map[string]*template.Template
}

// Handler serves the dashboard; every page but the login asks for a machine token, kept in a cookie.
func Handler(brains remote.Brains, log *slog.Logger) (http.Handler, error) {
	d := &Dashboard{brains: brains, log: log, pages: map[string]*template.Template{}}
	layout, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	for _, page := range []string{"library", "note", "edit", "new", "graph", "corrections", "history", "change", "skills", "login"} {
		t, err := template.Must(layout.Clone()).ParseFS(templateFS, "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		d.pages[page] = t
	}
	static, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(http.FileServerFS(static))))
	mux.HandleFunc("POST /blocks/{path...}", d.session((*Dashboard).saveBlock))
	mux.HandleFunc("POST /description/{path...}", d.session((*Dashboard).saveDescription))
	mux.HandleFunc("POST /rename/{path...}", d.session((*Dashboard).rename))
	mux.HandleFunc("POST /move/{path...}", d.session((*Dashboard).move))
	mux.HandleFunc("POST /delete/{path...}", d.session((*Dashboard).deleteNote))
	mux.HandleFunc("POST /ops/{op}/undo", d.session((*Dashboard).undoOp))
	mux.HandleFunc("GET /login", d.login)
	mux.HandleFunc("POST /login", d.loginSubmit)
	mux.HandleFunc("POST /logout", d.logout)
	mux.HandleFunc("GET /{$}", d.session((*Dashboard).library))
	mux.HandleFunc("GET /notes/{path...}", d.session((*Dashboard).note))
	mux.HandleFunc("POST /notes/{path...}", d.session((*Dashboard).save))
	mux.HandleFunc("GET /edit/{path...}", d.session((*Dashboard).edit))
	mux.HandleFunc("GET /new", d.session((*Dashboard).newForm))
	mux.HandleFunc("POST /new", d.session((*Dashboard).create))
	mux.HandleFunc("GET /graph", d.session((*Dashboard).graph))
	mux.HandleFunc("GET /corrections", d.session((*Dashboard).corrections))
	mux.HandleFunc("GET /history", d.session((*Dashboard).history))
	mux.HandleFunc("GET /changes/{seq}", d.session((*Dashboard).change))
	mux.HandleFunc("POST /changes/{seq}/undo", d.session((*Dashboard).undo))
	mux.HandleFunc("POST /changes/{seq}/restore", d.session((*Dashboard).restore))
	mux.HandleFunc("GET /skills", d.session((*Dashboard).skills))
	return securityHeaders(mux), nil
}

type handler func(d *Dashboard, w http.ResponseWriter, r *http.Request, machine string)

// session lets a request through to the brain its cookie's token opens, and refuses cross-site form posts.
func (d *Dashboard) session(next handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var s remote.Session
		if c, err := r.Cookie(cookieName); err == nil {
			if s, err = d.brains.Auth(c.Value); err != nil {
				d.fail(w, err)
				return
			}
		}
		if s.Store == nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		next(&Dashboard{brains: d.brains, store: s.Store, account: s.Account, log: d.log, pages: d.pages}, w, r, s.Machine)
	}
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func (d *Dashboard) login(w http.ResponseWriter, r *http.Request) {
	d.render(w, "login", page{Title: "Sign in", Next: safeNext(r.URL.Query().Get("next"))})
}

func (d *Dashboard) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	next := safeNext(r.FormValue("next"))
	s, err := d.brains.Auth(token)
	if err != nil {
		d.fail(w, err)
		return
	}
	if token == "" || s.Store == nil {
		w.WriteHeader(http.StatusUnauthorized)
		d.render(w, "login", page{Title: "Sign in", Next: next, Error: "That token is not one this server knows. Create one with wong serve --new-token <name>."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (d *Dashboard) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// safeNext keeps a redirect target inside the dashboard.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func (d *Dashboard) render(w http.ResponseWriter, name string, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := d.pages[name].ExecuteTemplate(w, "layout.html", p); err != nil {
		d.log.Error("dashboard render", "page", name, "err", err)
	}
}

func (d *Dashboard) fail(w http.ResponseWriter, err error) {
	d.log.Error("dashboard", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; style-src-attr 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func cacheFor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") == assetVersion {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		next.ServeHTTP(w, r)
	})
}

// assetVersion changes whenever a static file does, so browsers fetch the new one.
var assetVersion = func() string {
	h := sha256.New()
	fs.WalkDir(staticFS, ".", func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			data, _ := staticFS.ReadFile(p)
			h.Write([]byte(p))
			h.Write(data)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

var funcs = template.FuncMap{
	"day": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		if t.Year() != time.Now().Year() {
			return t.Local().Format("2 Jan 2006")
		}
		return t.Local().Format("2 Jan")
	},
	"stamp": func(t time.Time) string { return t.Local().Format("2 Jan 15:04") },
	"when": func(t time.Time) string {
		if y, m, d := t.Local().Date(); time.Now().Format("2006-01-02") == time.Date(y, m, d, 0, 0, 0, 0, time.Local).Format("2006-01-02") {
			return t.Local().Format("15:04")
		}
		return t.Local().Format("2 Jan")
	},
	"ago": ago,
	"days": func(t time.Time) int {
		if t.IsZero() {
			return 0
		}
		return max(0, int(time.Since(t).Hours()/24))
	},
	"notePath": notePath,
	"asset":    func(name string) string { return "/static/" + name + "?v=" + assetVersion },
	"folder":   folderOf,
	"title":    func(p string) string { return strings.TrimSuffix(path.Base(p), ".md") },
	"signer":   signer,
	"who":      who,
	"icon": func(name string) template.HTML {
		return template.HTML(`<svg class="i" aria-hidden="true"><use href="/static/icons.svg?v=` + assetVersion + `#` + template.HTMLEscapeString(name) + `"></use></svg>`)
	},
	"lineOp": func(op lineOp) string { return [...]string{"same", "added", "removed"}[op] },
	"viewIcon": func(id string) string {
		return map[string]string{"": "list-filter", "pending": "triangle-alert", "old": "clock", "week": "calendar"}[id]
	},
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "never"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// folderOf names the shelf of a path: "me", a project id, or the path's own folder.
func folderOf(p string) string {
	if id, ok := remote.NoteProject(p); ok {
		return id
	}
	return path.Dir(p)
}

// signer says who made a change: a machine, or you in this dashboard.
func signer(machine string) string {
	if strings.HasPrefix(machine, dashboardPrefix) {
		return "you, in the dashboard"
	}
	return machine
}

// who is signer in a few words, for tight places.
func who(machine string) string {
	if strings.HasPrefix(machine, dashboardPrefix) {
		return "you"
	}
	return machine
}

const dashboardPrefix = "dashboard:"
