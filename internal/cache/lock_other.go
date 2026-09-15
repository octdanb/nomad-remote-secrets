//go:build !unix

package cache

import "context"

// Lock is a no-op on platforms without advisory file locking. Callers treat a
// false result as "proceed unlocked", which is the pre-locking behaviour.
func (c *Cache) Lock(ctx context.Context, key string) (func(), bool) {
	return func() {}, false
}
