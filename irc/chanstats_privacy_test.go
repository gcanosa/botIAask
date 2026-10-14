package irc

import (
	"strings"
	"testing"

	"botIAask/config"
)

func TestChanStatsRefusesKeyedChannelsForNonAdmins(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "test", Nickname: "bot", Channels: []config.IRChannel{
		{Name: "#staff", Password: "k"}, {Name: "#pub"},
	}}}
	n, out := newCaptureNetwork(t, cfg)

	n.handleChanStatsCommand("alice", "alice", "!chanstats #Staff 7", false)
	if got := out.all(); len(got) != 1 || !strings.Contains(got[0], "no stats available") {
		t.Fatalf("keyed channel must be refused, got %q", got)
	}

	n.handleChanStatsCommand("alice", "alice", "!chanstats #pub 7", false)
	if got := out.all(); len(got) < 2 || strings.Contains(got[len(got)-1], "no stats available") {
		t.Fatalf("unkeyed channel must work, got %q", got)
	}

	before := len(out.all())
	n.handleChanStatsCommand("alice", "alice", "!chanstats #staff 7", true)
	if got := out.all(); len(got) == before || strings.Contains(got[before], "no stats available") {
		t.Fatalf("admin must be allowed, got %q", got)
	}
}
