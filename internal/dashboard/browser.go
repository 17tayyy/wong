//go:build !js

package dashboard

//go:generate sh -c "GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o web/wong.wasm ../../cmd/wongweb && gzip -9nf web/wong.wasm && cp $GOROOT/lib/wasm/wasm_exec.js web/"

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/remote"
)

// webFS holds the service worker and, when the build generated them, the dashboard compiled for the browser and the
// script that runs it.
//
//go:embed web
var webFS embed.FS

// browserBundle tells whether this build carries the dashboard for the browser, which a sealed brain needs to open in one.
func browserBundle() bool {
	_, err := fs.Stat(webFS, "web/wong.wasm.gz")
	return err == nil
}

func (d *Dashboard) browserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /browser/token", d.session((*Dashboard).browserToken))
	if !browserBundle() {
		return
	}
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'")
		serveWeb(w, r, "web/sw.js", "text/javascript; charset=utf-8", "")
	})
	mux.HandleFunc("GET /static/web/wasm_exec.js", func(w http.ResponseWriter, r *http.Request) {
		serveWeb(w, r, "web/wasm_exec.js", "text/javascript; charset=utf-8", "")
	})
	mux.HandleFunc("GET /static/web/wong.wasm", func(w http.ResponseWriter, r *http.Request) {
		serveWeb(w, r, "web/wong.wasm.gz", "application/wasm", "gzip")
	})
}

// serveWeb serves a file of the browser bundle, revalidated each time by its content so a new version reaches the
// worker as soon as it starts again.
func serveWeb(w http.ResponseWriter, r *http.Request, name, kind, encoding string) {
	data, err := webFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(data)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", kind)
	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// browserToken gives a browser that opens a sealed brain a token of its own, under a name no machine has, so asking
// for its key never takes another's.
func (d *Dashboard) browserToken(w http.ResponseWriter, r *http.Request, machine string) {
	if sealed, err := d.store.Sealed(); err != nil || !sealed {
		http.Error(w, "this brain is not end-to-end encrypted", http.StatusConflict)
		return
	}
	tokens, err := d.store.Tokens()
	if err != nil {
		d.fail(w, err)
		return
	}
	taken := map[string]bool{}
	for _, t := range tokens {
		taken[t.Name] = true
	}
	base := browserName(r.UserAgent())
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	token, err := d.store.NewToken(name)
	if err != nil {
		d.fail(w, err)
		return
	}
	d.log.Info("browser token", "name", name, "by", machine)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"token": token, "name": name})
}

// browserName names a browser after what it is and where it runs, as its user agent tells: "safari-on-iphone".
func browserName(agent string) string {
	browser := "browser"
	for _, b := range []struct{ mark, name string }{{"Edg/", "edge"}, {"Firefox/", "firefox"}, {"FxiOS", "firefox"}, {"CriOS", "chrome"}, {"Chrome/", "chrome"}, {"Safari/", "safari"}} {
		if strings.Contains(agent, b.mark) {
			browser = b.name
			break
		}
	}
	for _, o := range []struct{ mark, name string }{{"iPhone", "iphone"}, {"iPad", "ipad"}, {"Android", "android"}, {"Windows", "windows"}, {"Macintosh", "mac"}, {"Linux", "linux"}} {
		if strings.Contains(agent, o.mark) {
			return remote.MachineName(browser + "-on-" + o.name)
		}
	}
	return browser
}
