// Package ttlcache is a tiny size-bounded cache with per-entry expiry, for answers that are
// expensive to fetch (several upstream HTTP calls) and change slowly (release versions).
package ttlcache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	v   V
	exp time.Time
}

// Cache holds at most Max entries, each valid for TTL. The zero Cache is unusable; set TTL (and Max).
type Cache[K comparable, V any] struct {
	TTL time.Duration
	Max int // 0 = 1024

	mu sync.Mutex
	m  map[K]entry[V]
}

// Get returns the cached value for k if it has not expired.
func (c *Cache[K, V]) Get(k K) (v V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		delete(c.m, k)
		return v, false
	}
	return e.v, true
}

// Put stores v for k. When the cache is full, expired entries are dropped first, then the entry
// closest to expiry.
func (c *Cache[K, V]) Put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[K]entry[V])
	}
	max := c.Max
	if max <= 0 {
		max = 1024
	}
	if _, exists := c.m[k]; !exists && len(c.m) >= max {
		now := time.Now()
		var oldest K
		var oldestExp time.Time
		first := true
		for key, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, key)
				continue
			}
			if first || e.exp.Before(oldestExp) {
				oldest, oldestExp, first = key, e.exp, false
			}
		}
		if len(c.m) >= max && !first {
			delete(c.m, oldest)
		}
	}
	c.m[k] = entry[V]{v: v, exp: time.Now().Add(c.TTL)}
}
