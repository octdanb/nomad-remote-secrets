// Package cache is a file-backed cache for fetched secrets. Nomad executes
// the provider binary once per secret fetch, so any caching has to live
// outside the process. Entries are stored as root-only (0600) JSON files
// named by SHA-256 of the cache key, so the op:// paths themselves do not
// appear in file names.
//
// The cache serves two purposes: it keeps repeated fetches of the same
// reference (job restarts, multiple tasks sharing a secret) from hammering
// the Connect server, and — via Stale — it lets a deploy succeed with the
// last known value when the Connect server is briefly unreachable.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Cache stores secret key/value maps on disk with a freshness TTL.
type Cache struct {
	dir string
	ttl time.Duration
	now func() time.Time
}

type entry struct {
	FetchedAt time.Time         `json:"fetched_at"`
	Values    map[string]string `json:"values"`
}

// New opens (creating if needed) a cache rooted at dir. A ttl of zero or
// less disables freshness — Get will never hit, though Put still records
// entries for Stale fallback.
func New(dir string, ttl time.Duration) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating cache directory %s: %w", dir, err)
	}
	// The directory may predate us with looser permissions; tighten them.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restricting cache directory %s: %w", dir, err)
	}
	return &Cache{dir: dir, ttl: ttl, now: time.Now}, nil
}

// Get returns the cached values for key if they are fresher than the TTL.
func (c *Cache) Get(key string) (map[string]string, bool) {
	e, err := c.read(key)
	if err != nil || c.ttl <= 0 {
		return nil, false
	}
	if c.now().Sub(e.FetchedAt) > c.ttl {
		return nil, false
	}
	return e.Values, true
}

// Stale returns the cached values for key regardless of the TTL, as long as
// the entry is no older than maxAge. It also reports the entry's age so the
// caller can log how stale the served value is.
func (c *Cache) Stale(key string, maxAge time.Duration) (map[string]string, time.Duration, bool) {
	e, err := c.read(key)
	if err != nil {
		return nil, 0, false
	}
	age := c.now().Sub(e.FetchedAt)
	if maxAge > 0 && age > maxAge {
		return nil, 0, false
	}
	return e.Values, age, true
}

// Put records values for key. Writes are atomic (temp file + rename) so a
// concurrent fetch never observes a partial entry.
func (c *Cache) Put(key string, values map[string]string) error {
	data, err := json.Marshal(entry{FetchedAt: c.now(), Values: values})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, ".put-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path(key))
}

func (c *Cache) read(key string) (*entry, error) {
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, err
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (c *Cache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}
