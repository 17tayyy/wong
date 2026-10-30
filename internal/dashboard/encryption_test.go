package dashboard

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/remote"
	"github.com/17tayyy/wong/internal/seal"
)

// fakeKeyring is the keyring a machine that holds the key would see, recording what the page asked of it.
type fakeKeyring struct {
	keys     remote.Keys
	self     []byte
	approved []string
	cloud    bool
}

func (f *fakeKeyring) Keys(context.Context) (remote.Keys, error) { return f.keys, nil }
func (f *fakeKeyring) Self() []byte                              { return f.self }
func (f *fakeKeyring) Approve(_ context.Context, req remote.KeyRequest) error {
	f.approved = append(f.approved, req.Device)
	return nil
}
func (f *fakeKeyring) LetCloudAIs(context.Context) error  { f.cloud = true; return nil }
func (f *fakeKeyring) StopCloudAIs(context.Context) error { f.cloud = false; return nil }

func encryptionBrowser(t *testing.T, store *remote.Store, keyring Keyring) browser {
	t.Helper()
	token, err := store.NewToken("browser")
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Devices: remote.NewDevices(), SignIns: remote.NewSignIns(), Keyring: keyring})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	b := browser{t, srv, &http.Client{Jar: jar}}
	if code, _ := b.post("/login", url.Values{"token": {token}}, srv.URL); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}
	return b
}

func newDevice(t *testing.T) *seal.Device {
	t.Helper()
	d, err := seal.NewDevice()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEncryptionInABrowserWithoutTheKey(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cloud, err := remote.NewCloudAIs(filepath.Join(t.TempDir(), "cloud-key"))
	if err != nil {
		t.Fatal(err)
	}
	store.SetCloudAIs(cloud)
	key, _ := seal.NewKey()
	desktop, laptop := newDevice(t), newDevice(t)
	wrapped, _ := seal.Wrap(key, desktop.Public())
	if err := store.StartSeal("desktop", desktop.Public(), wrapped, desktop.Public(), wrapped, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RequestKey("laptop", laptop.Public()); err != nil {
		t.Fatal(err)
	}
	forCloud, _ := seal.Wrap(key, cloud.Public())
	if err := store.LetCloudAIs("desktop", forCloud); err != nil {
		t.Fatal(err)
	}
	b := encryptionBrowser(t, store, nil)

	code, body := b.get("/encryption")
	if code != http.StatusOK || !strings.Contains(body, "desktop") || !strings.Contains(body, seal.Fingerprint(laptop.Public())) {
		t.Fatalf("the page does not list the machines and the code of the one waiting: %d", code)
	}
	if strings.Contains(body, `value="approve"`) || strings.Contains(body, `value="cloud-on"`) || !strings.Contains(body, `value="cloud-off"`) {
		t.Error("a browser without the key is offered what needs it, or not the way to turn the cloud AIs off")
	}
	if code, _ := b.post("/encryption", url.Values{"do": {"approve"}, "machine": {"laptop"}, "code": {seal.Fingerprint(laptop.Public())}}, b.srv.URL); code != http.StatusForbidden {
		t.Errorf("a browser without the key approved a machine: %d", code)
	}
	if code, body := b.post("/encryption", url.Values{"do": {"cloud-off"}}, b.srv.URL); code != http.StatusOK || !strings.Contains(body, "cannot read it any more") {
		t.Errorf("turning the cloud AIs off from the browser: %d", code)
	}
	if keys, _ := store.Keys("browser"); keys.CloudAIs {
		t.Error("the server still opens the brain for the cloud AIs")
	}
}

func TestEncryptionOnAMachineThatHoldsTheKey(t *testing.T) {
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	self, laptop := newDevice(t), newDevice(t)
	keyring := &fakeKeyring{self: self.Public(), keys: remote.Keys{Sealed: true, Wrapped: []byte("wrapped"),
		Holders: []remote.KeyRequest{{Device: "desktop", Public: self.Public()}},
		Pending: []remote.KeyRequest{{Device: "laptop", Public: laptop.Public()}}}}
	b := encryptionBrowser(t, store, keyring)

	_, body := b.get("/encryption")
	if !strings.Contains(body, "This machine") || !strings.Contains(body, `value="approve"`) || !strings.Contains(body, `name="understood"`) {
		t.Fatal("the machine that holds the key is not offered to approve and to let the cloud AIs in")
	}
	if _, body := b.post("/encryption", url.Values{"do": {"approve"}, "machine": {"laptop"}, "code": {"0000 0000 0000 0000 0000 0000"}}, b.srv.URL); len(keyring.approved) > 0 || !strings.Contains(body, "asked again with another code") {
		t.Error("a machine was approved with a code other than the one it waits with")
	}
	if _, body := b.post("/encryption", url.Values{"do": {"approve"}, "machine": {"laptop"}, "code": {seal.Fingerprint(laptop.Public())}}, b.srv.URL); len(keyring.approved) != 1 || !strings.Contains(body, "laptop has the brain&#39;s key now") {
		t.Errorf("approving the machine with its code: %v", keyring.approved)
	}
	if code, _ := b.post("/encryption", url.Values{"do": {"cloud-on"}}, b.srv.URL); code != http.StatusBadRequest || keyring.cloud {
		t.Error("the cloud AIs were let in without the person saying they understood")
	}
	if _, body := b.post("/encryption", url.Values{"do": {"cloud-on"}, "understood": {"yes"}}, b.srv.URL); !keyring.cloud || !strings.Contains(body, "can read the brain now") {
		t.Error("the cloud AIs were not let in")
	}
}
