package irc

import (
	"strings"
	"testing"
	"time"

	"botIAask/config"
	"botIAask/meta"
)

func newTestBot(cfg *config.Config) *Bot {
	b := NewBot(cfg, nil)
	return b
}

// newTestNetwork builds a *Bot plus a detached (never-connected) *ircNetwork for the
// first configured network, for unit tests that exercise ircNetwork-receiver methods
// without a live IRC connection.
func newTestNetwork(cfg *config.Config) *ircNetwork {
	b := newTestBot(cfg)
	name := "test"
	if len(cfg.IRC.Networks) > 0 {
		name = cfg.IRC.Networks[0].Name
	}
	return &ircNetwork{Bot: b, name: name, channelMembers: make(map[string]map[string]struct{})}
}

func TestFormatQuitMessage_default(t *testing.T) {
	n := newTestNetwork(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test", Nickname: "TestBot"}}},
	})
	n.startTime = time.Now().Add(-30 * time.Minute)
	s := n.FormatQuitMessage("")
	if !strings.Contains(s, meta.Name) || !strings.Contains(s, meta.Version) {
		t.Fatalf("default quit message: %q", s)
	}
	if !strings.Contains(s, "Uptime:") {
		t.Fatalf("expected Uptime: in %q", s)
	}
}

func TestFormatQuitMessage_template(t *testing.T) {
	n := newTestNetwork(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{
			Name:        "test",
			Nickname:    "N",
			QuitMessage: "{nickname} | {name} {version} | {uptime}",
		}}},
	})
	n.startTime = time.Now().Add(-time.Second)
	s := n.FormatQuitMessage("")
	if !strings.Contains(s, "N") || !strings.Contains(s, meta.Name) {
		t.Fatalf("template quit message: %q", s)
	}
}

func TestFormatQuitMessage_override(t *testing.T) {
	n := newTestNetwork(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{{Name: "test", QuitMessage: "ignore me"}}},
	})
	if got := n.FormatQuitMessage("  bye  "); got != "bye" {
		t.Fatalf("override: %q", got)
	}
}
