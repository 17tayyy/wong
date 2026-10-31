package dashboard

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/remote"
)

func TestEachBrowserGetsATokenOfItsOwn(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	b := encryptionBrowser(t, store, nil)
	token := func() (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, b.srv.URL+"/browser/token", nil)
		req.Header.Set("Origin", b.srv.URL)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Mobile Safari/537.36")
		resp, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct{ Token, Name string }
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode == http.StatusOK && (out.Token == "" || !strings.HasPrefix(out.Token, "wong_")) {
			t.Errorf("no token in the answer: %+v", out)
		}
		return resp.StatusCode, out.Name
	}
	if code, _ := token(); code != http.StatusConflict {
		t.Errorf("a brain in the clear gave a browser a token: %d", code)
	}
	device := newDevice(t)
	if err := store.StartSeal("desktop", device.Public(), []byte("w"), device.Public(), []byte("w"), 0); err != nil {
		t.Fatal(err)
	}
	if _, first := token(); first != "chrome-on-android" {
		t.Errorf("the browser is named %q", first)
	}
	if _, second := token(); second != "chrome-on-android-2" {
		t.Errorf("a second browser took the first one's name: %q", second)
	}
	if code, _ := b.post("/browser/token", url.Values{}, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("another site got a token for this browser: %d", code)
	}
}

func TestBrowserNames(t *testing.T) {
	for agent, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1": "safari-on-iphone",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36":                       "chrome-on-mac",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36 Edg/141.0":                   "edge-on-windows",
		"Mozilla/5.0 (X11; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0":                                                                  "firefox-on-linux",
		"curl/8.0": "browser",
	} {
		if got := browserName(agent); got != want {
			t.Errorf("browserName(%q) = %q, want %q", agent, got, want)
		}
	}
}
