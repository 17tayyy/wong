package seal

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func mustKey(t *testing.T) *Key {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpens(t *testing.T) {
	k := mustKey(t)
	for _, c := range []struct{ path, content string }{
		{"projects/acme/deploy-runbook.md", "---\nname: deploy-runbook\n---\nShip on Tuesdays.\n"},
		{"me/use-uv.md", ""},
		{"projects/acmé/ñandú.md", strings.Repeat("x", 1<<20)},
	} {
		id, blob, err := k.Seal(c.path, []byte(c.content))
		if err != nil {
			t.Fatal(err)
		}
		if id != k.ID(c.path) || bytes.Contains(blob, []byte(c.path)) {
			t.Errorf("%s: the id is not the path's, or the blob shows the path", c.path)
		}
		path, content, err := k.Open(id, blob)
		if err != nil || path != c.path || string(content) != c.content {
			t.Errorf("%s did not come back as it went: %q, %v", c.path, path, err)
		}
	}
}

func TestOpenRefusesWhatWasTouched(t *testing.T) {
	k := mustKey(t)
	id, blob, err := k.Seal("projects/acme/a.md", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := k.Seal("projects/acme/b.md", []byte("other"))
	flipped := bytes.Clone(blob)
	flipped[len(flipped)-1] ^= 1
	newer := bytes.Clone(blob)
	newer[0] = version + 1
	for name, c := range map[string]struct {
		key  *Key
		id   string
		blob []byte
	}{
		"another file's id":   {k, other, blob},
		"a changed byte":      {k, id, flipped},
		"another brain's key": {mustKey(t), id, blob},
		"a cut blob":          {k, id, blob[:10]},
		"an unknown version":  {k, id, newer},
		"nothing":             {k, id, nil},
	} {
		if _, _, err := c.key.Open(c.id, c.blob); err != ErrOpen {
			t.Errorf("%s opened: %v", name, err)
		}
	}
}

func TestSealRefusesEmptyAndLongPaths(t *testing.T) {
	k := mustKey(t)
	for _, path := range []string{"", strings.Repeat("a", maxPath+1)} {
		if _, _, err := k.Seal(path, nil); err == nil {
			t.Errorf("a path of %d bytes was sealed", len(path))
		}
	}
}

func TestIDsAndSumsDependOnTheKey(t *testing.T) {
	a, b := mustKey(t), mustKey(t)
	if a.ID("me/x.md") != a.ID("me/x.md") || a.Sum([]byte("x")) != a.Sum([]byte("x")) {
		t.Error("the same key gave two ids or sums for the same input")
	}
	if a.ID("me/x.md") == b.ID("me/x.md") || a.Sum([]byte("x")) == b.Sum([]byte("x")) {
		t.Error("two brains share an id or a sum")
	}
	if a.ID("me/x.md") == a.ID("me/y.md") || a.ID("x") == a.Sum([]byte("x")) {
		t.Error("ids collide, or an id equals the sum of the same bytes")
	}
}

func TestEachDeviceOpensWhatWasWrappedForIt(t *testing.T) {
	k := mustKey(t)
	laptop, err := NewDevice()
	if err != nil {
		t.Fatal(err)
	}
	stranger, _ := NewDevice()
	wrapped, err := Wrap(k, laptop.Public())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := laptop.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	again, err := DeviceFrom(saved)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Unwrap(wrapped)
	if err != nil || got.ID("me/x.md") != k.ID("me/x.md") {
		t.Fatalf("the laptop could not open its wrapped key: %v", err)
	}
	if _, err := stranger.Unwrap(wrapped); err != ErrOpen {
		t.Errorf("another device opened the laptop's wrapped key: %v", err)
	}
	if _, err := Wrap(k, []byte("not a key")); err == nil {
		t.Error("a key was wrapped for garbage")
	}
}

func TestRecoveryCodeOpensTheKey(t *testing.T) {
	k := mustKey(t)
	recovery := NewRecovery()
	if !regexp.MustCompile(`^([A-Z2-7]{4}-){7}[A-Z2-7]{4}$`).MatchString(recovery) {
		t.Fatalf("a recovery code should be eight groups of four: %s", recovery)
	}
	device, err := RecoveryDevice(recovery)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := Wrap(k, device.Public())
	if err != nil {
		t.Fatal(err)
	}
	typed, err := RecoveryDevice(strings.ToLower(strings.ReplaceAll(recovery, "-", " ")))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := typed.Unwrap(wrapped); err != nil || got.ID("me/x.md") != k.ID("me/x.md") {
		t.Errorf("the code, typed in lower case with spaces, did not open the key: %v", err)
	}
	wrong, _ := RecoveryDevice(NewRecovery())
	if _, err := wrong.Unwrap(wrapped); err != ErrOpen {
		t.Errorf("another recovery code opened the key: %v", err)
	}
	for _, bad := range []string{"", "ABCD", recovery + "-ABCD", "!!!!-" + recovery[5:]} {
		if _, err := RecoveryDevice(bad); err == nil {
			t.Errorf("%q was taken as a recovery code", bad)
		}
	}
}

func TestFingerprint(t *testing.T) {
	a, _ := NewDevice()
	b, _ := NewDevice()
	fa := Fingerprint(a.Public())
	if !regexp.MustCompile(`^(\d{4} ){5}\d{4}$`).MatchString(fa) || fa != Fingerprint(a.Public()) {
		t.Errorf("a fingerprint should be six stable groups of four digits: %s", fa)
	}
	if fa == Fingerprint(b.Public()) {
		t.Error("two devices share a fingerprint")
	}
}

func TestTheFormatDoesNotChange(t *testing.T) {
	raw := make([]byte, keySize)
	for i := range raw {
		raw[i] = byte(i)
	}
	k, err := KeyFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	device, err := RecoveryDevice("AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-GGGG-HHHH")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ got, want string }{
		"id":          {k.ID("projects/acme/deploy-runbook.md"), "a259ed0508c699b24409f2628347ee2da4a3e91a3074d1472d2fb4d7e1c08f49"},
		"sum":         {k.Sum([]byte("Ship on Tuesdays.\n")), "58c014b51fab281dbc8d95c332013fa5d76e0492f482b230f19746ff0ffa2d5c"},
		"fingerprint": {Fingerprint(device.Public()), "1518 2529 4498 9629 1674 8763"},
	} {
		if c.got != c.want {
			t.Errorf("the %s changed: %s", name, c.got)
		}
	}
	if _, err := KeyFrom(raw[:31]); err == nil {
		t.Error("a key of 31 bytes was taken")
	}
}
