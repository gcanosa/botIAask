package irc

import (
	"testing"
	"time"
)

// TestRateLimiter_NetworkIsolation guards the cross-network collision bug: the same
// sender+target pair (most commonly a PM, where target is the bot's own nickname and can be
// identical across networks) must not share one budget between two IRC networks.
func TestRateLimiter_NetworkIsolation(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const burst = 2

	if !rl.Allow("alpha", "bob", "botnick", 0, burst) {
		t.Fatal("alpha: 1st call should be allowed")
	}
	if !rl.Allow("alpha", "bob", "botnick", 0, burst) {
		t.Fatal("alpha: 2nd call should be allowed")
	}
	if rl.Allow("alpha", "bob", "botnick", 0, burst) {
		t.Fatal("alpha: 3rd call should be rate-limited")
	}

	// Same sender+target on a different network must have its own, unexhausted budget.
	if !rl.Allow("beta", "bob", "botnick", 0, burst) {
		t.Fatal("beta: budget must be independent of alpha's")
	}
}

// TestRateLimiter_EvictStale guards unbounded growth: an entry untouched for well past the
// window must eventually be evicted rather than living forever in the map.
func TestRateLimiter_EvictStale(t *testing.T) {
	rl := NewRateLimiter(time.Millisecond)
	rl.Allow("alpha", "bob", "botnick", 0, 5)
	if len(rl.limits) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(rl.limits))
	}

	// Force the entry stale (older than window*10) and the sweep due (older than window),
	// bypassing real sleeps.
	rl.mu.Lock()
	for _, v := range rl.limits {
		v.lastReset = time.Now().Add(-time.Hour)
	}
	rl.lastSweep = time.Now().Add(-time.Hour)
	rl.mu.Unlock()

	rl.Allow("gamma", "carol", "botnick", 0, 5) // triggers evictStaleLocked as a side effect

	rl.mu.RLock()
	_, staleStillThere := rl.limits["alpha\x00bob\x00botnick"]
	rl.mu.RUnlock()
	if staleStillThere {
		t.Fatal("stale entry was not evicted")
	}
}
