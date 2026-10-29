package remote

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDevicesHandOutATokenOnceApproved(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	devices := NewDevices()
	secret, code, err := devices.Ask("laptop", "192.0.2.7")
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 8 || strings.Trim(code, codeAlphabet) != "" {
		t.Fatalf("code = %q", code)
	}
	typed := strings.ToLower(ShowCode(code))
	if dev, ok := devices.Find(" " + typed + " "); !ok || dev.Name != "laptop" || dev.From != "192.0.2.7" {
		t.Fatalf("Find(%q) = %+v, %v", typed, dev, ok)
	}
	if status, _, _, _ := devices.Poll(secret); status != DeviceWaiting {
		t.Errorf("before approval: %v", status)
	}
	if status, _, _, _ := devices.Poll("someone else's"); status != DeviceGone {
		t.Errorf("an unknown secret: %v", status)
	}
	if !devices.Approve(typed, store, "work-laptop") {
		t.Fatal("Approve refused a waiting machine")
	}
	if _, ok := devices.Find(code); ok {
		t.Error("an approved machine is still waiting")
	}
	status, s, token, err := devices.Poll(secret)
	if err != nil || status != DeviceApproved || s.Machine != "work-laptop" {
		t.Fatalf("Poll = %v, %+v, %v", status, s, err)
	}
	if s, err := store.Auth(token); err != nil || s.Machine != "work-laptop" {
		t.Errorf("the token opens %+v, %v", s, err)
	}
	if status, _, token, _ := devices.Poll(secret); status != DeviceGone || token != "" {
		t.Errorf("the token was handed out twice: %v", status)
	}
}

func TestDevicesTurnedAwayOrExpired(t *testing.T) {
	devices := NewDevices()
	now := time.Now()
	devices.now = func() time.Time { return now }
	denied, code, _ := devices.Ask("laptop", "192.0.2.7")
	if !devices.Deny(code) {
		t.Fatal("Deny refused a waiting machine")
	}
	if status, _, _, _ := devices.Poll(denied); status != DeviceDenied {
		t.Errorf("a machine turned away: %v", status)
	}
	late, code, _ := devices.Ask("laptop", "192.0.2.7")
	now = now.Add(deviceTTL + time.Second)
	if _, ok := devices.Find(code); ok {
		t.Error("an expired code is still found")
	}
	if status, _, _, _ := devices.Poll(late); status != DeviceGone {
		t.Errorf("an expired code: %v", status)
	}
	for range maxPerAddress {
		if _, _, err := devices.Ask("x", "192.0.2.7"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := devices.Ask("x", "192.0.2.7"); !errors.Is(err, ErrTooManyDevices) {
		t.Errorf("one machine too many from one address: %v", err)
	}
	for i := range maxDevices - maxPerAddress {
		if _, _, err := devices.Ask("x", fmt.Sprintf("198.51.100.%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := devices.Ask("x", "203.0.113.1"); !errors.Is(err, ErrTooManyDevices) {
		t.Errorf("one machine too many: %v", err)
	}
}

func TestMachineName(t *testing.T) {
	for in, want := range map[string]string{
		"Work-Laptop.local": "work-laptop",
		"DESKTOP_01":        "desktop-01",
		"  ":                "machine",
		"dashboard:you":     "dashboard-you",
	} {
		if got := MachineName(in); got != want {
			t.Errorf("MachineName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientIP(t *testing.T) {
	for _, c := range []struct{ remote, cf, xff, want string }{
		{"203.0.113.9:4000", "", "", "203.0.113.9"},
		{"203.0.113.9:4000", "198.51.100.1", "198.51.100.2", "203.0.113.9"},
		{"127.0.0.1:4000", "198.51.100.1", "198.51.100.2", "198.51.100.1"},
		{"172.18.0.3:4000", "", "192.0.2.66, 198.51.100.2", "198.51.100.2"},
		{"[::1]:4000", "not an address", "", "::1"},
	} {
		r := httptest.NewRequest("POST", "/api/device", nil)
		r.RemoteAddr = c.remote
		if c.cf != "" {
			r.Header.Set("Cf-Connecting-Ip", c.cf)
		}
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r); got != c.want {
			t.Errorf("ClientIP from %s, %q, %q = %s, want %s", c.remote, c.cf, c.xff, got, c.want)
		}
	}
}
