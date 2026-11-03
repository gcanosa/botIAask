package irc

import (
	"strings"
	"testing"
	"time"

	"botIAask/config"
)

func TestGuardCommand(t *testing.T) {
	n, out := accessNetwork(t, nil)
	n.quit = make(chan struct{})
	n.addNames("#c", "@bot +alice ~carol bob!u@h")
	n.handleGuardCommand("#c", "alice", strings.Fields("on 3"))
	n.handleGuardCommand("alice", "alice", strings.Fields("#c OFF"))
	n.handleGuardCommand("#d", "alice", []string{"on"}) // no member count: no +l
	n.handleGuardCommand("#d", "alice", []string{"off"})
	n.handleGuardCommand("#c", "alice", strings.Fields("off 3"))
	got := strings.Join(out.all(), "\n")
	want := strings.Join([]string{
		"MODE #c +mv alice", "MODE #c +l 7", "PRIVMSG #c Guard on for #c: moderated, at most 3 joins every 30s.",
		"MODE #c -ml", "PRIVMSG alice Guard off for #c.",
		"MODE #d +mv alice", "PRIVMSG #d Guard on for #d: moderated, at most 5 joins every 30s.",
		"MODE #d -ml", "PRIVMSG #d Guard off for #d.",
		"PRIVMSG #c Usage: !guard [#chan] on [joins 1-50, default 5] | off",
	}, "\n")
	if got != want {
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
	n, out := accessNetwork(t, nil)
	n.quit = make(chan struct{})
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
