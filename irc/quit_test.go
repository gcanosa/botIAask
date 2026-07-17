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

func TestFormatQuitMessage_default(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Nickname: "TestBot"},
	})
	b.startTime = time.Now().Add(-30 * time.Minute)
	s := b.FormatQuitMessage("")
	if !strings.Contains(s, meta.Name) || !strings.Contains(s, meta.Version) {
		t.Fatalf("default quit message: %q", s)
	}
	if !strings.Contains(s, "Uptime:") {
		t.Fatalf("expected Uptime: in %q", s)
	}
}

func TestFormatQuitMessage_template(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{
			Nickname:    "N",
			QuitMessage: "{nickname} | {name} {version} | {uptime}",
		},
	})
	b.startTime = time.Now().Add(-time.Second)
	s := b.FormatQuitMessage("")
	if !strings.Contains(s, "N") || !strings.Contains(s, meta.Name) {
		t.Fatalf("template quit message: %q", s)
	}
}

func TestFormatQuitMessage_override(t *testing.T) {
	b := newTestBot(&config.Config{IRC: config.IRCConfig{QuitMessage: "ignore me"}})
	if got := b.FormatQuitMessage("  bye  "); got != "bye" {
		t.Fatalf("override: %q", got)
	}
}
