package remote

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/17tayyy/wong/internal/seal"
)

// ErrCloudAIsOff marks a server that keeps no key to open brains for the AIs that run in the cloud.
var ErrCloudAIsOff = errors.New("this server cannot open brains for the AIs that run in the cloud")

const cloudIdle = 10 * time.Minute

// CloudAIs opens the sealed brains whose accounts let the AIs that run in the cloud, connected over MCP, read them.
// Such an account keeps its brain's key wrapped for the server's own key, which lives beside the brains and never in
// their backups; the server opens the brain in memory for each call and seals back what the call changed.
type CloudAIs struct {
	device *seal.Device
	mu     sync.Mutex
	open   map[*Store]*cloudBrain
}

// cloudBrain is a sealed brain the server opened for the cloud AIs, and when it was last used.
type cloudBrain struct {
	*Opened
	used time.Time
}

// NewCloudAIs loads the server's key for the cloud AIs from keyFile, making it the first time.
func NewCloudAIs(keyFile string) (*CloudAIs, error) {
	data, err := os.ReadFile(keyFile)
	if errors.Is(err, fs.ErrNotExist) {
		device, err := seal.NewDevice()
		if err != nil {
			return nil, err
		}
		if data, err = device.Bytes(); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return NewCloudAIs(keyFile)
		}
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	device, err := seal.DeviceFrom(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyFile, err)
	}
	return &CloudAIs{device: device, open: map[*Store]*cloudBrain{}}, nil
}

// Public is the key a machine wraps the brain's key for, to let the cloud AIs in.
func (c *CloudAIs) Public() []byte {
	return c.device.Public()
}

// run calls do with store's brain in the clear, as machine, and seals back into store what do changed. ok is false
// when the account did not let the cloud AIs in.
func (c *CloudAIs) run(store *Store, machine string, do func(*Store) error) (ok bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evict()
	var wrapped []byte
	err = store.db.QueryRow(`SELECT wrapped FROM cloud_ais`).Scan(&wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		c.forget(store)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	o := c.open[store]
	if o == nil {
		key, err := c.device.Unwrap(wrapped)
		if err != nil {
			return true, fmt.Errorf("the brain's key for the cloud AIs does not open: %w", err)
		}
		opened, err := open(key, storeSide{store})
		if err != nil {
			return true, err
		}
		o = &cloudBrain{Opened: opened}
		c.open[store] = o
	}
	defer func() {
		if err != nil {
			c.forget(store)
		}
	}()
	ctx := context.Background()
	if err := o.Refresh(ctx); err != nil {
		return true, err
	}
	o.used = time.Now()
	return true, errors.Join(do(o.Plain), o.Flush(ctx, machine))
}

// forget drops the brain of store from memory.
func (c *CloudAIs) forget(store *Store) {
	if o := c.open[store]; o != nil {
		o.Close()
		delete(c.open, store)
	}
}

func (c *CloudAIs) evict() {
	for store, o := range c.open {
		if time.Since(o.used) > cloudIdle {
			c.forget(store)
		}
	}
}

// OpenForCloudAIs calls do with the brain in the clear when it is sealed and its account let the AIs that run in the
// cloud read it, sealing back what do changed, as machine; ok is false when the brain stays closed to them.
func (s *Store) OpenForCloudAIs(machine string, do func(*Store) error) (ok bool, err error) {
	if s.cloud == nil {
		return false, nil
	}
	return s.cloud.run(s, machine, do)
}

// LetCloudAIs lets the AIs that run in the cloud read the sealed brain, with its key wrapped for the server by device,
// which must hold the key.
func (s *Store) LetCloudAIs(device string, wrapped []byte) error {
	if s.cloud == nil {
		return ErrCloudAIsOff
	}
	if _, err := s.cloud.device.Unwrap(wrapped); err != nil {
		return ErrNoKey
	}
	var held int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM keyring WHERE device = ? AND wrapped IS NOT NULL`, device).Scan(&held); err != nil {
		return err
	}
	if held == 0 {
		return ErrNoKey
	}
	_, err := s.db.Exec(`INSERT INTO cloud_ais (id, wrapped, device, created_at) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET wrapped = excluded.wrapped, device = excluded.device, created_at = excluded.created_at`, wrapped, device, utcNow())
	return err
}

// StopCloudAIs closes the sealed brain to the AIs that run in the cloud again: the server forgets its key.
func (s *Store) StopCloudAIs() error {
	if _, err := s.db.Exec(`DELETE FROM cloud_ais`); err != nil {
		return err
	}
	if s.cloud != nil {
		s.cloud.mu.Lock()
		s.cloud.forget(s)
		s.cloud.mu.Unlock()
	}
	return nil
}

// SetCloudAIs gives the store the server's key for the cloud AIs.
func (s *Store) SetCloudAIs(c *CloudAIs) { s.cloud = c }

func (s *Store) cursor() (int64, error) {
	var cursor sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(seq) FROM history`).Scan(&cursor)
	return cursor.Int64, err
}
