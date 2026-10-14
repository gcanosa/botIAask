package irc

import (
	"sync"
	"testing"
	"time"

	"botIAask/config"
	"botIAask/logger"
)

// sink collects what a test network "sent".
type sink struct {
	mu    sync.Mutex
	lines []string
}

func (s *sink) add(command, target, msg string) {
	s.mu.Lock()
	s.lines = append(s.lines, command+" "+target+" "+msg)
	s.mu.Unlock()
}

func (s *sink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// newCaptureNetwork returns a detached network whose output lands in the sink. It runs in a temp
// dir because sendPrivmsg also writes channel logs under ./logs, and disables pacing.
func newCaptureNetwork(t *testing.T, cfg *config.Config) (*ircNetwork, *sink) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Cleanup(logger.CloseAll) // handles are cached per relative path; this dir is about to vanish
	if len(cfg.IRC.Networks) == 0 {
		cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "test", Nickname: "bot"}}
	}
	if cfg.Bot.CommandPrefix == "" {
		cfg.Bot.CommandPrefix = "!"
	}
	n := newTestNetwork(cfg)
	s := &sink{}
	n.testSend = s.add
	n.pace = pacer{burst: 1 << 20, interval: time.Nanosecond}
	return n, s
}
