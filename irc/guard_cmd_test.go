package irc

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"botIAask/config"
)

// guardNetwork is a capture network whose config edits are saved to a temp file and synced,
// like the real rehash does (ApplyLiveConfig -> syncGuards).
func guardNetwork(t *testing.T) (*ircNetwork, *sink) {
	n, out := accessNetwork(t, nil)
	n.quit = make(chan struct{})
	path := filepath.Join(t.TempDir(), "config.yaml")
	n.configPath = path
	n.rehashHook = func(string) error {
		c, err := config.LoadConfig(path)
		if err == nil {
			n.cfg.Store(c)
			n.syncGuards("")
		}
		return err
	}
	return n, out
}

func TestGuardCommand(t *testing.T) {
	n, out := guardNetwork(t)
	n.addNames("#c", "@bot +alice ~carol bob!u@h")
	n.handleGuardCommand("#c", "alice", strings.Fields("on 3"))
	if g := n.netCfg().Guard; len(g) != 1 || g[0] != (config.ChannelGuard{Channel: "#c", Joins: 3, Moderate: true}) {
		t.Fatalf("not saved: %+v", g)
	}
	n.handleGuardCommand("alice", "alice", strings.Fields("#c OFF"))
	n.handleGuardCommand("#d", "alice", []string{"on"}) // no member count: no +l
	n.handleGuardCommand("#d", "alice", []string{"off"})
	n.handleGuardCommand("#c", "alice", strings.Fields("off 3"))
	got := strings.Join(out.all(), "\n")
	want := strings.Join([]string{
		"MODE #c +m", "MODE #c +l 7", "MODE #c +v alice", "PRIVMSG #c Guard on for #c: moderated, at most 3 joins every 30s.",
		"MODE #c -ml", "PRIVMSG alice Guard off for #c.",
		"MODE #d +m", "MODE #d +v alice", "PRIVMSG #d Guard on for #d: moderated, at most 5 joins every 30s.",
		"MODE #d -ml", "PRIVMSG #d Guard off for #d.",
		"PRIVMSG #c Usage: !guard [#chan] on [joins 1-50, default 5] | off",
	}, "\n")
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
	if len(n.guards) != 0 || len(n.netCfg().Guard) != 0 {
		t.Fatalf("guards left: %v %v", n.guards, n.netCfg().Guard)
	}
}

// Config edits (the dashboard path): unchanged entries are left alone, a dropped +m sends -m,
// a removed limit-only guard sends -l, and a forced sync (366 after a rejoin) re-sends the modes.
func TestSyncGuards(t *testing.T) {
	n, out := guardNetwork(t)
	n.addNames("#c", "bot alice")
	set := func(g ...config.ChannelGuard) {
		n.getCfg().IRC.Networks[0].Guard = g
		n.syncGuards("")
	}
	set(config.ChannelGuard{Channel: "#c", Joins: 2, Moderate: true})
	set(config.ChannelGuard{Channel: "#C", Joins: 2, Moderate: true}) // same guard
	set(config.ChannelGuard{Channel: "#c", Joins: 2})                 // +l only now
	n.syncGuards("#C")                                                // rejoin
	set()
	want := "MODE #c +m\nMODE #c +l 4\nMODE #c -m\nMODE #c +l 4\nMODE #c +l 4\nMODE #c -l"
	if got := strings.Join(out.all(), "\n"); got != want {
		t.Fatalf("got:\n%s", got)
	}
	if len(n.guards) != 0 {
		t.Fatalf("guards left running: %v", n.guards)
	}
}

// The loop re-bases +l on the live member count, both up and down.
func TestGuardLimitFollowsMembers(t *testing.T) {
	old := guardInterval
	guardInterval = 5 * time.Millisecond
	t.Cleanup(func() { guardInterval = old })
	n, out := guardNetwork(t)
	n.addNames("#c", "bot alice")
	n.handleGuardCommand("#c", "alice", strings.Fields("on 2"))
	waitFor := func(line string) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			if strings.Contains(strings.Join(out.all(), "\n"), line) {
				return
			}
		}
		t.Fatalf("no %q in:\n%s", line, strings.Join(out.all(), "\n"))
	}
	waitFor("MODE #c +l 4")
	n.addNames("#c", "x y z")
	waitFor("MODE #c +l 7")
	n.membersMu.Lock()
	delete(n.channelMembers["#c"], "x")
	n.membersMu.Unlock()
	waitFor("MODE #c +l 6")
	n.handleGuardCommand("#c", "alice", []string{"off"})
}

// Logged-in admins get adminBurstFactor times the burst; everyone else keeps the configured one.
func TestRateLimitAdminBurst(t *testing.T) {
	n, _ := accessNetwork(t, nil)
	n.getCfg().Bot.RateLimiting = &config.RateLimitConfig{Enabled: true, Burst: 2, Window: 60}
	n.rateLimiter = NewRateLimiter(time.Minute)
	n.loggedInAdmins[adminSessionKey(n.name, "admin")] = true
	count := func(nick string) (ok int) {
		for range 20 {
			if !n.rateLimited("#c", nick) {
				ok++
			}
		}
		return ok
	}
	if got := count("user"); got != 2 {
		t.Fatalf("user allowed %d, want 2", got)
	}
	if got := count("admin"); got != 2*adminBurstFactor {
		t.Fatalf("admin allowed %d, want %d", got, 2*adminBurstFactor)
	}
}
