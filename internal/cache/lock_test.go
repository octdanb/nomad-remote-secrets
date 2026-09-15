//go:build unix

package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openTwice returns two independent Cache handles on the same directory. flock
// is held per open file description, so two handles contend exactly as two
// plugin processes do.
func openTwice(t *testing.T) (*Cache, *Cache) {
	t.Helper()
	dir := t.TempDir()
	a, err := New(dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestLockExcludesASecondHolder(t *testing.T) {
	// Arrange
	a, b := openTwice(t)
	releaseA, locked := a.Lock(context.Background(), "k")
	if !locked {
		t.Fatal("first Lock did not take the lock")
	}
	defer releaseA()

	// Act - the second holder must not get in while the first holds it.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	releaseB, locked := b.Lock(ctx, "k")
	defer releaseB()

	// Assert
	if locked {
		t.Fatal("second Lock succeeded while the first was held")
	}
}

func TestLockIsGrantedAfterRelease(t *testing.T) {
	// Arrange
	a, b := openTwice(t)
	releaseA, locked := a.Lock(context.Background(), "k")
	if !locked {
		t.Fatal("first Lock did not take the lock")
	}

	// Act - release from another goroutine so the waiter has to block first.
	go func() {
		time.Sleep(50 * time.Millisecond)
		releaseA()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	releaseB, locked := b.Lock(ctx, "k")
	defer releaseB()

	// Assert
	if !locked {
		t.Fatal("Lock never granted after the holder released")
	}
}

func TestLockOnDifferentKeysDoesNotBlock(t *testing.T) {
	// Arrange - different references must resolve concurrently; serialising
	// every key would make a multi-reference fetch as slow as the sum of its
	// parts.
	a, b := openTwice(t)
	releaseA, _ := a.Lock(context.Background(), "k1")
	defer releaseA()

	// Act
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	releaseB, locked := b.Lock(ctx, "k2")
	defer releaseB()

	// Assert
	if !locked {
		t.Fatal("Lock on a different key blocked")
	}
}

func TestLockWaitStopsWhenContextIsCancelled(t *testing.T) {
	// Arrange - a waiter must never outlive the fetch's deadline, or it would
	// be killed by Nomad instead of reporting a timeout.
	a, b := openTwice(t)
	releaseA, _ := a.Lock(context.Background(), "k")
	defer releaseA()

	// Act
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	releaseB, locked := b.Lock(ctx, "k")
	defer releaseB()
	elapsed := time.Since(start)

	// Assert
	if locked {
		t.Fatal("Lock succeeded while held")
	}
	if elapsed > time.Second {
		t.Fatalf("Lock waited %v after its context expired", elapsed)
	}
}

func TestLockFileDoesNotRevealTheKey(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	c, err := New(dir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Act
	release, _ := c.Lock(context.Background(), "op://Prod/database/password")
	release()

	// Assert - a 64-character hex digest plus ".lock".
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 lock file, got %d", len(entries))
	}
	if name := entries[0].Name(); filepath.Ext(name) != ".lock" || len(name) != 69 {
		t.Fatalf("unexpected lock file name %q", name)
	}
}
