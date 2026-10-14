package web

import (
	"os"
	"runtime"
	"testing"
	"time"

	"botIAask/config"
)

// Disabling and re-enabling the dashboard (rehash) creates a fresh Server each time; Close must
// release its goroutines and database handles instead of accumulating them.
func TestServerCloseReleasesGoroutines(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	// warm up: first use starts shared runtime/driver goroutines that never go away
	if s, err := NewServer(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		s.Close()
	}
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		s, err := NewServer(cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		s.Close() // idempotent
	}
	var now int
	for i := 0; i < 100; i++ {
		if now = runtime.NumGoroutine(); now <= base+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines grew from %d to %d after 5 create/close cycles", base, now)
}
