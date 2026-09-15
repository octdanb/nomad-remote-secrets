//go:build unix

package cache

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// A contended lock is retried on a backoff rather than with a blocking flock,
// so the wait always observes ctx cancellation and never outlives the fetch.
const (
	minLockPoll = 25 * time.Millisecond
	maxLockPoll = 250 * time.Millisecond
)

// Lock takes an advisory exclusive lock covering key and returns a function
// that releases it. The returned bool reports whether the lock was actually
// held: callers use it to decide whether a second cache read is worth doing,
// but must call the release function either way.
//
// Locking is best-effort by design. If the lock file cannot be created, the
// filesystem does not support advisory locking, or ctx is cancelled while
// waiting, Lock reports false and the caller proceeds unlocked — a lock is an
// optimisation, and failing to take one must never fail a deploy. The kernel
// drops a flock when the process exits, so a crashed holder cannot wedge the
// cache.
func (c *Cache) Lock(ctx context.Context, key string) (func(), bool) {
	f, err := os.OpenFile(c.pathFor(key, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, false
	}

	wait := minLockPoll
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, true
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return func() {}, false
		}

		select {
		case <-ctx.Done():
			f.Close()
			return func() {}, false
		case <-time.After(wait):
		}
		if wait *= 2; wait > maxLockPoll {
			wait = maxLockPoll
		}
	}
}
