package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetFreshAndExpired(t *testing.T) {
	c, err := New(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	c.now = func() time.Time { return now }

	want := map[string]string{"password": "hunter2"}
	if err := c.Put("k", want); err != nil {
		t.Fatal(err)
	}

	got, ok := c.Get("k")
	if !ok || got["password"] != "hunter2" {
		t.Fatalf("Get = %v, %v; want fresh hit", got, ok)
	}

	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("k"); ok {
		t.Fatal("Get hit after TTL expiry")
	}
}

func TestGetMiss(t *testing.T) {
	c, err := New(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("nope"); ok {
		t.Fatal("Get hit for missing key")
	}
}

func TestZeroTTLNeverFresh(t *testing.T) {
	c, err := New(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put("k", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("k"); ok {
		t.Fatal("Get hit with zero TTL")
	}
	// ...but the entry is still available for stale fallback.
	if _, _, ok := c.Stale("k", time.Hour); !ok {
		t.Fatal("Stale miss with zero TTL")
	}
}

func TestStaleRespectsMaxAge(t *testing.T) {
	c, err := New(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c.now = func() time.Time { return now }

	if err := c.Put("k", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(3 * time.Hour)
	if _, age, ok := c.Stale("k", 24*time.Hour); !ok || age.Round(time.Hour) != 3*time.Hour {
		t.Fatalf("Stale = age %v, ok %v; want hit at ~3h", age, ok)
	}
	if _, _, ok := c.Stale("k", time.Hour); ok {
		t.Fatal("Stale hit beyond max age")
	}
}

func TestFilePermissions(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put("k", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 cache file, got %d", len(entries))
	}
	info, err := os.Stat(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache file mode = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("cache dir mode = %o, want 700", perm)
	}
}

func TestKeyNotExposedInFilename(t *testing.T) {
	dir := t.TempDir()
	c, err := New(dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put("op://Prod/database/password", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if name := e.Name(); name != "" && (filepath.Ext(name) != ".json" || len(name) != 69) {
			t.Fatalf("unexpected cache file name %q", name)
		}
	}
}
