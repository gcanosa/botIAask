package irc

import (
	"strings"
	"testing"

	"botIAask/ai"
	"botIAask/config"
)

// A failing LM Studio request must not put its endpoint (internal host:port) into the channel.
func TestAskErrorDoesNotLeakEndpoint(t *testing.T) {
	cfg := &config.Config{}
	cfg.Bot.CommandPrefix, cfg.Bot.CommandName = "!", "ask"
	n, out := newCaptureNetwork(t, cfg)
	n.aiClient = ai.NewClient("http://127.0.0.1:1/v1", "m") // connection refused straight away

	n.handleCommand("#c", "!ask hello", "alice", "alice!u@h")

	lines := out.all()
	if len(lines) != 1 {
		t.Fatalf("want exactly one reply, got %q", lines)
	}
	if strings.Contains(lines[0], "127.0.0.1") || strings.Contains(lines[0], ":1/") {
		t.Fatalf("reply leaks the endpoint: %q", lines[0])
	}
	if !strings.Contains(lines[0], "alice") {
		t.Fatalf("reply should address the sender: %q", lines[0])
	}
}
