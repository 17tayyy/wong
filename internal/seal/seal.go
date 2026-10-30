// Package seal encrypts a brain end to end. Its key lives only on the account's devices, wrapped for each of them
// with HPKE, and every file reaches the server as a blob under a name the server cannot read back to a path.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	version   = 1
	keySize   = 32
	codeSize  = 20
	maxPath   = 4096
	wrapInfo  = "wong brain key v1"
	codeInfo  = "wong recovery v1"
	notesInfo = "wong notes v1"
	idsInfo   = "wong ids v1"
	sumsInfo  = "wong sums v1"
)

// ErrOpen is returned when a blob or a wrapped key does not open: a wrong key, a wrong id, or bytes changed on the way.
var ErrOpen = errors.New("seal: cannot open")

var (
	kem  = hpke.DHKEM(ecdh.X25519())
	kdf  = hpke.HKDFSHA256()
	aead = hpke.AES256GCM()
	code = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// Key is a brain's key, with the keys it derives for files, their ids and their sums.
type Key struct {
	raw   []byte
	notes cipher.AEAD
	ids   []byte
	sums  []byte
}

// NewKey makes a brain's key.
func NewKey() (*Key, error) {
	return KeyFrom(random(keySize))
}

// KeyFrom rebuilds a brain's key from its 32 bytes.
func KeyFrom(raw []byte) (*Key, error) {
	if len(raw) != keySize {
		return nil, fmt.Errorf("seal: a brain key has %d bytes, not %d", keySize, len(raw))
	}
	derive := func(info string) []byte {
		b, err := hkdf.Key(sha256.New, raw, nil, info, keySize)
		if err != nil {
			panic(err)
		}
		return b
	}
	block, err := aes.NewCipher(derive(notesInfo))
	if err != nil {
		return nil, err
	}
	notes, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{raw: append([]byte(nil), raw...), notes: notes, ids: derive(idsInfo), sums: derive(sumsInfo)}, nil
}

// ID is the name the server keeps a file under, the same on every device and unreadable without the key.
func (k *Key) ID(path string) string {
	return mac(k.ids, []byte(path))
}

// Sum stands in for a file's sha in the sync, so the server can tell versions apart without learning their content.
func (k *Key) Sum(content []byte) string {
	return mac(k.sums, content)
}

// Seal encrypts a file's path and content into a blob bound to its id.
func (k *Key) Seal(path string, content []byte) (id string, blob []byte, err error) {
	if len(path) == 0 || len(path) > maxPath {
		return "", nil, fmt.Errorf("seal: a path of %d bytes", len(path))
	}
	id = k.ID(path)
	plain := binary.BigEndian.AppendUint16(nil, uint16(len(path)))
	plain = append(append(plain, path...), content...)
	nonce := random(k.notes.NonceSize())
	blob = append([]byte{version}, nonce...)
	return id, k.notes.Seal(blob, nonce, plain, bound(id)), nil
}

// Open decrypts a blob kept under id, refusing one that was changed or moved to another id.
func (k *Key) Open(id string, blob []byte) (path string, content []byte, err error) {
	n := k.notes.NonceSize()
	if len(blob) < 1+n+k.notes.Overhead() || blob[0] != version {
		return "", nil, ErrOpen
	}
	plain, err := k.notes.Open(nil, blob[1:1+n], blob[1+n:], bound(id))
	if err != nil || len(plain) < 2 {
		return "", nil, ErrOpen
	}
	size := int(binary.BigEndian.Uint16(plain))
	if 2+size > len(plain) || k.ID(string(plain[2:2+size])) != id {
		return "", nil, ErrOpen
	}
	return string(plain[2 : 2+size]), plain[2+size:], nil
}

// Device is the key pair of one device of the account: a machine with wong, or a browser.
type Device struct {
	private hpke.PrivateKey
}

// NewDevice makes a device's key pair.
func NewDevice() (*Device, error) {
	private, err := kem.GenerateKey()
	if err != nil {
		return nil, err
	}
	return &Device{private}, nil
}

// DeviceFrom reads a device's private key, as Bytes wrote it.
func DeviceFrom(b []byte) (*Device, error) {
	private, err := kem.NewPrivateKey(b)
	if err != nil {
		return nil, err
	}
	return &Device{private}, nil
}

// Bytes is the device's private key, to keep on the device and nowhere else.
func (d *Device) Bytes() ([]byte, error) {
	return d.private.Bytes()
}

// Public is the device's public key, which the server relays so another device can wrap the brain's key for it.
func (d *Device) Public() []byte {
	return d.private.PublicKey().Bytes()
}

// Wrap encrypts the brain's key for a device's public key.
func Wrap(k *Key, public []byte) ([]byte, error) {
	pk, err := kem.NewPublicKey(public)
	if err != nil {
		return nil, err
	}
	return hpke.Seal(pk, kdf, aead, []byte(wrapInfo), k.raw)
}

// Unwrap opens the brain's key that another device wrapped for this one.
func (d *Device) Unwrap(wrapped []byte) (*Key, error) {
	raw, err := hpke.Open(d.private, kdf, aead, []byte(wrapInfo), wrapped)
	if err != nil {
		return nil, ErrOpen
	}
	return KeyFrom(raw)
}

// Fingerprint is what both devices show while one links the other, for the person to compare: if the server swapped
// the new device's public key for its own, the two would differ.
func Fingerprint(public []byte) string {
	sum := sha256.Sum256(public)
	groups := make([]string, 6)
	for i := range groups {
		groups[i] = fmt.Sprintf("%04d", binary.BigEndian.Uint16(sum[2*i:])%10000)
	}
	return strings.Join(groups, " ")
}

// NewRecovery makes a recovery code: written down by the person, it opens the brain's key when every device is lost.
func NewRecovery() string {
	s := code.EncodeToString(random(codeSize))
	var groups []string
	for len(s) > 0 {
		groups, s = append(groups, s[:4]), s[4:]
	}
	return strings.Join(groups, "-")
}

// RecoveryDevice is the device a recovery code stands for, so the brain's key is wrapped for it like for any other.
func RecoveryDevice(recovery string) (*Device, error) {
	b, err := code.DecodeString(strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(recovery)))
	if err != nil || len(b) != codeSize {
		return nil, errors.New("seal: that is not a recovery code")
	}
	ikm, err := hkdf.Key(sha256.New, b, nil, codeInfo, keySize)
	if err != nil {
		return nil, err
	}
	private, err := kem.DeriveKeyPair(ikm)
	if err != nil {
		return nil, err
	}
	return &Device{private}, nil
}

// bound is what a blob's encryption is tied to besides its key: the format's version and the file's id.
func bound(id string) []byte {
	return append([]byte{version}, id...)
}

func mac(key, data []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}
