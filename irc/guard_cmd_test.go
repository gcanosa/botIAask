package irc

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"botIAask/config"
)

// guardNetwork is a capture network whose config edits are saved to a temp file and synced,
// like the real rehash does (ApplyLiveConfig -> syncGuards). Grace, floor, threshold and
// lowering delay are off, so +l is exactly members+joins (the tests below the knobs).
func guardNetwork(t *testing.T) (*ircNetwork, *sink) {
	n, out := guardNetworkTuned(t, config.GuardSettings{GraceSeconds: -1, MinLimit: -1, LowerDelaySeconds: -1, Threshold: -1})
	return n, out
}

func guardNetworkTuned(t *testing.T, gs config.GuardSettings) (*ircNetwork, *sink) {
	n, out := accessNetwork(t, nil)
	n.getCfg().IRC.Networks[0].GuardSettings = gs
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

// Config edits (the dashboard path): unchanged entries are left alone, a dropped +m sends -m
// (the +l already set is kept), a removed limit-only guard sends -l, and a forced sync (366
// after a rejoin) re-sends the modes.
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
	want := "MODE #c +m\nMODE #c +l 4\nMODE #c -m\nMODE #c +l 4\nMODE #c -l"
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

// simGuard drives one guard on a virtual clock: join adds users (failing the test if the +l the
// bot set would refuse them with 471), tick runs the 30s check.
type simGuard struct {
	t    *testing.T
	n    *ircNetwork
	run  *guardRun
	now  time.Time
	next int // user counter
}

func (s *simGuard) advance(d time.Duration) {
	for end := s.now.Add(d); !s.now.After(end); s.now = s.now.Add(time.Second) {
		if s.now.Sub(time.Time{})%(30*time.Second) == 0 {
			s.n.guardMu.Lock()
			s.n.applyGuardLimit(s.run, s.now)
			s.n.guardMu.Unlock()
		}
	}
}

// join tries count users spread over d; returns how many the +l refused.
func (s *simGuard) join(count int, d time.Duration) (refused int) {
	for range count {
		s.advance(d / time.Duration(count))
		if s.run.cur > 0 && s.n.memberCount("#c") >= s.run.cur {
			refused++
			continue
		}
		s.next++
		s.n.addNames("#c", "u"+strconv.Itoa(s.next))
	}
	return refused
}

// The ircd-restart case: the bot rejoins an empty channel, then 20 users reconnect within
// 10s. Nobody may be refused; after the grace period +l comes back at the floor, follows a
// real flood (refusing past the margin) and later steps back down in large-enough steps.
func TestGuardRestartNeverLocksOut(t *testing.T) {
	n, out := guardNetworkTuned(t, config.GuardSettings{})
	n.addNames("#c", "bot ChanServ")
	n.getCfg().IRC.Networks[0].Guard = []config.ChannelGuard{{Channel: "#c", Joins: 5}}
	n.syncGuards("#c") // 366 after the rejoin
	s := &simGuard{t: t, n: n, run: n.guards["#c"], now: time.Time{}.Add(30 * time.Second)}
	s.run.graceUntil = s.now.Add(180 * time.Second) // syncGuards used the wall clock
	t.Cleanup(func() { n.syncGuards("") })

	s.advance(9 * time.Second)
	if r := s.join(20, 10*time.Second); r != 0 {
		t.Fatalf("%d of 20 reconnecting users refused (+l %d)", r, s.run.cur)
	}
	if got := strings.Join(out.all(), "\n"); got != "MODE #c -l" {
		t.Fatalf("during grace, got:\n%s", got)
	}
	s.advance(200 * time.Second) // grace over: 22 members, floor 30
	if s.run.cur != 30 {
		t.Fatalf("+l after grace = %d, want floor 30", s.run.cur)
	}
	if r := s.join(40, 10*time.Second); r == 0 {
		t.Fatalf("join flood: nobody refused, +l %d", s.run.cur)
	}
	s.advance(30 * time.Second)
	if m := n.memberCount("#c"); s.run.cur < m+5 || s.run.cur > m+5+2 {
		t.Fatalf("+l after flood tick = %d, want %d members + 5 joins (+2 headroom)", s.run.cur, m)
	}
	high := s.run.cur
	n.membersMu.Lock()
	for i := 21; i <= s.next; i++ { // the bots get kicked: 22 members again
		delete(n.channelMembers["#c"], "u"+strconv.Itoa(i))
	}
	n.membersMu.Unlock()
	s.advance(4 * time.Minute)
	if s.run.cur != high {
		t.Fatalf("lowered before the delay: +l %d -> %d", high, s.run.cur)
	}
	s.advance(5 * time.Minute)
	if s.run.cur != 30 {
		t.Fatalf("+l after lowering = %d, want floor 30", s.run.cur)
	}
	prev := 0
	for _, l := range out.all()[1:] { // every +l move is more than the threshold (2)
		v, _ := strconv.Atoi(strings.TrimPrefix(l, "MODE #c +l "))
		if d := v - prev; d >= -2 && d <= 2 {
			t.Fatalf("+l moved by %d: %v", d, out.all())
		}
		prev = v
	}
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
