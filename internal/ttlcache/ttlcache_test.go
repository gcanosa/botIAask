package ttlcache

import (
	"fmt"
	"testing"
	"time"
)

func TestExpiryAndBound(t *testing.T) {
	c := &Cache[string, int]{TTL: 30 * time.Millisecond, Max: 3}
	c.Put("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("fresh entry missing: %v %v", v, ok)
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("expired entry served")
	}

	c.TTL = time.Hour
	for i := 0; i < 10; i++ {
		c.Put(fmt.Sprint(i), i)
		time.Sleep(time.Millisecond) // distinct expiry times
	}
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	if n != 3 {
		t.Fatalf("cache holds %d entries, want Max 3", n)
	}
	if _, ok := c.Get("9"); !ok {
		t.Fatal("newest entry was evicted")
	}
	if _, ok := c.Get("0"); ok {
		t.Fatal("oldest entry survived")
	}
}
