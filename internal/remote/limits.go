package remote

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits bound what one brain may take; a zero leaves its bound off.
type Limits struct {
	Size     int64
	File     int64
	Requests int
}

// ErrLimit marks a change or request refused because it goes past one of the brain's limits.
var ErrLimit = fmt.Errorf("over a limit")

func (s *Store) SetLimits(l Limits) { s.limits = l }

func (s *Store) Limits() Limits { return s.limits }

// Size is the room the brain takes on disk, its history included.
func (s *Store) Size() (int64, error) {
	var pages, size int64
	if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		return 0, err
	}
	if err := s.db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		return 0, err
	}
	return pages * size, nil
}

// room refuses changes with a file larger than a file may be, or that would grow the brain past its size.
func (s *Store) room(pushes []Upload) error {
	var adding int64
	for _, p := range pushes {
		if s.limits.File > 0 && int64(len(p.Content)) > s.limits.File {
			return refuse(ErrLimit, "%s takes %s, more than the %s a file may take on this server.", p.Path, Bytes(int64(len(p.Content))), Bytes(s.limits.File))
		}
		adding += 2 * int64(len(p.Content))
	}
	if s.limits.Size == 0 || adding == 0 {
		return nil
	}
	size, err := s.Size()
	if err != nil {
		return err
	}
	if size+adding > s.limits.Size {
		return refuse(ErrLimit, "This brain takes %s of the %s it may, and the change does not fit. Deleting notes frees no room, since the brain keeps their history: ask whoever runs the server for more.", Bytes(size), Bytes(s.limits.Size))
	}
	return nil
}

// Bytes writes a size the way people read it.
func Bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatInt(n>>10, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

// ParseBytes reads a size such as 100M, 2M, 512K or 1G; a bare number is bytes.
func ParseBytes(text string) (int64, error) {
	s := strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(text)), "B")
	unit := int64(1)
	if s != "" {
		if u, ok := map[byte]int64{'K': 1 << 10, 'M': 1 << 20, 'G': 1 << 30}[s[len(s)-1]]; ok {
			s, unit = s[:len(s)-1], u
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q: use a number with K, M or G, such as 100M", text)
	}
	return n * unit, nil
}

// pace keeps each brain to its requests a minute, with as many in a burst.
type pace struct {
	mu      sync.Mutex
	buckets map[*Store]*allowance
}

type allowance struct {
	tokens float64
	last   time.Time
}

func (p *pace) allow(s *Store) bool {
	limit := float64(s.limits.Requests)
	if limit == 0 {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	a, ok := p.buckets[s]
	if !ok {
		a = &allowance{tokens: limit, last: now}
		p.buckets[s] = a
	}
	a.tokens = min(limit, a.tokens+now.Sub(a.last).Minutes()*limit)
	a.last = now
	if a.tokens < 1 {
		return false
	}
	a.tokens--
	return true
}
