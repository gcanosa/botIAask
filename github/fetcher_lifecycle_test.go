package github

import (
	"path/filepath"
	"testing"
	"time"

	"botIAask/config"
)

// stubBot is a minimal BotInterface that never reports connected, so Fetch() never
// actually runs — these tests only exercise the Start/Stop loop lifecycle.
type stubBot struct{}

func (stubBot) Broadcast(channels []string, message string) {}
func (stubBot) IsConnected() bool                           { return false }

func newTestFetcher(t *testing.T, cfg *config.Config) *Fetcher {
	t.Helper()
	d, err := NewDatabase(filepath.Join(t.TempDir(), "github_test.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	c, err := NewCryptor(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("NewCryptor: %v", err)
	}
	return NewFetcher(cfg, stubBot{}, d, c)
}

// TestFetcherStopDuringConnectWait verifies Stop() unblocks Start() promptly even while
// Start() is still parked in the "wait for bot to connect" loop (mirrors the identical
// rss/fetcher_lifecycle_test.go case, same underlying loop shape).
func TestFetcherStopDuringConnectWait(t *testing.T) {
	cfg := &config.Config{GitHubTracker: config.GitHubTrackerConfig{Enabled: true, IntervalMinutes: 60}}
	f := newTestFetcher(t, cfg)

	done := make(chan struct{})
	go func() {
		f.Start()
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	f.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return within 2s of Stop() during connect-wait")
	}
}

func TestApplyConfigSameLoopNoRestart(t *testing.T) {
	cfg := &config.Config{GitHubTracker: config.GitHubTrackerConfig{Enabled: true, IntervalMinutes: 60}}
	f := newTestFetcher(t, cfg)

	before := f.stopChan
	newCfg := &config.Config{GitHubTracker: config.GitHubTrackerConfig{
		Enabled: true, IntervalMinutes: 60,
		Repos: []config.GitHubTrackerRepoConfig{{Owner: "a", Repo: "b"}},
	}}
	f.ApplyConfig(newCfg)

	f.mu.Lock()
	after := f.stopChan
	gotRepos := len(f.cfg.GitHubTracker.Repos)
	f.mu.Unlock()

	if before != after {
		t.Fatal("ApplyConfig restarted the loop (stopChan changed) despite enabled/interval being unchanged")
	}
	if gotRepos != 1 {
		t.Fatalf("ApplyConfig did not swap in the new config: got %d repos", gotRepos)
	}
}

func TestApplyConfigIntervalChangeRestarts(t *testing.T) {
	cfg := &config.Config{GitHubTracker: config.GitHubTrackerConfig{Enabled: true, IntervalMinutes: 60}}
	f := newTestFetcher(t, cfg)

	before := f.stopChan
	newCfg := &config.Config{GitHubTracker: config.GitHubTrackerConfig{Enabled: true, IntervalMinutes: 30}}
	f.ApplyConfig(newCfg)

	f.mu.Lock()
	after := f.stopChan
	f.mu.Unlock()

	if before == after {
		t.Fatal("ApplyConfig did not restart the loop despite an interval change")
	}
}
