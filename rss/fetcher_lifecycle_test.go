package rss

import (
	"testing"
	"time"

	"botIAask/config"
)

// stubBot is a minimal BotInterface that never reports connected, so Fetch()
// never actually runs — these tests only exercise the Start/Stop loop lifecycle.
type stubBot struct{}

func (stubBot) Broadcast(channels []string, message string) {}
func (stubBot) IsConnected() bool                            { return false }

func newTestFetcher(t *testing.T, cfg *config.Config) *Fetcher {
	t.Helper()
	db, err := NewDatabase(t.TempDir() + "/rss_test.db")
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewFetcher(cfg, stubBot{}, db)
}

// TestFetcherStopDuringConnectWait verifies Stop() unblocks Start() promptly even
// while Start() is still parked in the "wait for bot to connect" loop — the bug was
// that loop used a bare time.Sleep and never observed the closed stopChan.
func TestFetcherStopDuringConnectWait(t *testing.T) {
	cfg := &config.Config{RSS: config.RSSConfig{Enabled: true, IntervalMinutes: 60}}
	f := newTestFetcher(t, cfg)

	done := make(chan struct{})
	go func() {
		f.Start()
		close(done)
	}()

	// Give Start() time to enter the connect-wait loop, then stop it.
	time.Sleep(50 * time.Millisecond)
	f.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return within 2s of Stop() during connect-wait")
	}
}

// TestApplyConfigSameLoopNoRestart verifies a rehash that doesn't change RSS.Enabled
// or the ticker interval swaps the config in place without closing/replacing stopChan
// (which would otherwise leave the running loop's captured channel stale but alive).
func TestApplyConfigSameLoopNoRestart(t *testing.T) {
	cfg := &config.Config{RSS: config.RSSConfig{Enabled: true, IntervalMinutes: 60, URLShortener: "is.gd"}}
	f := newTestFetcher(t, cfg)

	before := f.stopChan
	newCfg := &config.Config{RSS: config.RSSConfig{Enabled: true, IntervalMinutes: 60, URLShortener: "tinyurl"}}
	f.ApplyConfig(newCfg)

	f.mu.Lock()
	after := f.stopChan
	gotShortener := f.cfg.RSS.URLShortener
	f.mu.Unlock()

	if before != after {
		t.Fatal("ApplyConfig restarted the loop (stopChan changed) despite enabled/interval being unchanged")
	}
	if gotShortener != "tinyurl" {
		t.Fatalf("ApplyConfig did not swap in the new config: got shortener %q", gotShortener)
	}
}

// TestApplyConfigIntervalChangeRestarts verifies a rehash that changes the ticker
// interval does replace stopChan (the loop must actually restart to pick up the new ticker).
func TestApplyConfigIntervalChangeRestarts(t *testing.T) {
	cfg := &config.Config{RSS: config.RSSConfig{Enabled: true, IntervalMinutes: 60}}
	f := newTestFetcher(t, cfg)

	before := f.stopChan
	newCfg := &config.Config{RSS: config.RSSConfig{Enabled: true, IntervalMinutes: 30}}
	f.ApplyConfig(newCfg)

	f.mu.Lock()
	after := f.stopChan
	f.mu.Unlock()

	if before == after {
		t.Fatal("ApplyConfig did not restart the loop despite an interval change")
	}
}
