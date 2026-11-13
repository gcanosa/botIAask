package irc

import (
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
	"botIAask/internal/ircfold"
)

// guardInterval is how often a guarded channel's +l is re-based on its member count.
// ponytail: fixed 30s, make it an argument if admins ever need a different pace.
var guardInterval = 30 * time.Second

// guardRun is one running guard loop; its settings are compared with config on every sync.
// cur/graceUntil/highSince are the +l state, touched only under guardMu.
type guardRun struct {
	channel  string
	step     int // margin: joins allowed above the member count
	moderate bool
	interval int // seconds, 0 = guardInterval
	minLimit int // ChannelGuard.MinLimit, 0 = tune.MinLimit
	tune     config.GuardSettings
	stop     chan struct{}

	cur        int       // +l the bot set, 0 = none
	graceUntil time.Time // no +l before this (set on (re)join)
	highSince  time.Time // since when cur is more than Threshold above what's needed
}

func (r *guardRun) floor() int {
	if r.minLimit > 0 {
		return r.minLimit
	}
	return r.tune.MinLimit
}

// next decides the +l for members users at now: the new limit and why, or why == "" to leave
// it. Never sets one during the grace period; raises at once (with Threshold headroom) when
// members+step goes above it; lowers only after it stood more than Threshold too high for
// LowerDelaySeconds, then by half the excess per check (at least Threshold+1, so every MODE
// moves it by more than Threshold). Never below floor().
func (r *guardRun) next(members int, now time.Time) (int, string) {
	if members == 0 || now.Before(r.graceUntil) {
		return r.cur, ""
	}
	need := max(r.floor(), members+r.step)
	up := max(r.floor(), members+r.step+r.tune.Threshold)
	switch {
	case r.cur == 0:
		r.highSince = time.Time{}
		return up, fmt.Sprintf("no limit set: %d members + %d joins, floor %d", members, r.step, r.floor())
	case need > r.cur:
		r.highSince = time.Time{}
		return up, fmt.Sprintf("raise: %d members + %d joins > %d", members, r.step, r.cur)
	case r.cur-need <= r.tune.Threshold:
		r.highSince = time.Time{}
		return r.cur, ""
	case r.highSince.IsZero():
		r.highSince = now
		return r.cur, ""
	case now.Sub(r.highSince) < time.Duration(r.tune.LowerDelaySeconds)*time.Second:
		return r.cur, ""
	}
	excess := r.cur - need
	return r.cur - min(excess, max((excess+1)/2, r.tune.Threshold+1)),
		fmt.Sprintf("lower: %d above needed (%d members + %d joins, floor %d) since %s", excess, members, r.step, r.floor(), r.highSince.Format(time.TimeOnly))
}

// handleGuardCommand: !guard [#chan] on [joins] | off (admin session required, see bot.go).
// on: saves a moderated guard entry for the channel (irc.networks[].guard, also edited by the
// dashboard) and voices the caller (harmless when they're already +v/+h/+o/+q); the rehash that
// follows starts it via syncGuards. off: removes the entry, which sends -ml. Needs ops; the server
// answers 482 otherwise (logged in network.go).
func (b *ircNetwork) handleGuardCommand(target, sender string, args []string) {
	channel, rest, ok := channelArg(target, args)
	step := config.GuardDefaultJoins
	valid := ok && len(rest) >= 1 && len(rest) <= 2
	if valid && len(rest) == 2 {
		n, err := strconv.Atoi(rest[1])
		step, valid = n, err == nil && n >= 1 && n <= config.GuardMaxJoins && strings.EqualFold(rest[0], "on")
	}
	on := valid && strings.EqualFold(rest[0], "on")
	if !valid || (!on && !strings.EqualFold(rest[0], "off")) {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sguard [#chan] on [joins 1-%d, default %d] | off", b.pfx(), config.GuardMaxJoins, config.GuardDefaultJoins))
		return
	}
	err := b.editNetwork("!guard", func(n *config.IRCNetworkConfig) error {
		interval := 0 // keep a dashboard-set interval
		n.Guard = slices.DeleteFunc(n.Guard, func(g config.ChannelGuard) bool {
			if strings.EqualFold(g.Channel, channel) {
				interval = g.Interval
				return true
			}
			return false
		})
		if on {
			n.Guard = append(n.Guard, config.ChannelGuard{Channel: channel, Joins: step, Moderate: true, Interval: interval})
		}
		return nil
	})
	if err != nil {
		b.sendPrivmsg(target, "guard: "+err.Error())
		return
	}
	if !on {
		b.sendPrivmsg(target, fmt.Sprintf("Guard off for %s.", channel))
		return
	}
	b.chanop("MODE", channel, "+v", sender)
	every := guardInterval
	for _, g := range b.netCfg().Guard {
		if strings.EqualFold(g.Channel, channel) && g.Interval > 0 {
			every = time.Duration(g.Interval) * time.Second
		}
	}
	b.sendPrivmsg(target, fmt.Sprintf("Guard on for %s: moderated, at most %d joins every %s.", channel, step, every))
}

// syncGuards makes the running guard loops match this network's config: removed entries stop
// and lift their modes (-l, plus -m if they set it), new or changed ones start (+m when Moderate,
// then the +l from guardRun.next). force (a channel, or "") restarts that channel's guard even
// when unchanged: used after the bot (re)joins it (366 RPL_ENDOFNAMES). A forced restart sends
// -l and holds off +l for the grace period, so the reconnect wave after a server restart is
// never refused; a config change keeps the running guard's +l state and grace.
func (b *ircNetwork) syncGuards(force string) {
	nc := b.netCfg()
	want, err := config.NormalizeGuard(nc.Guard)
	if err != nil {
		log.Printf("irc[%s]: guard config ignored: %v", b.name, err)
	}
	tune := nc.GuardSettings.Resolved()
	find := func(key string) (config.ChannelGuard, bool) {
		for _, g := range want {
			if ircfold.Nick(g.Channel) == key {
				return g, true
			}
		}
		return config.ChannelGuard{}, false
	}
	forceKey := ircfold.Nick(force)
	now := time.Now()
	b.guardMu.Lock()
	defer b.guardMu.Unlock()
	prev := map[string]*guardRun{}
	for key, run := range b.guards {
		g, ok := find(key)
		forced := force != "" && key == forceKey
		if ok && g.Joins == run.step && g.Moderate == run.moderate && g.Interval == run.interval &&
			g.MinLimit == run.minLimit && tune == run.tune && !forced {
			continue
		}
		close(run.stop)
		delete(b.guards, key)
		switch {
		case !ok && run.moderate:
			b.chanop("MODE", run.channel, "-ml")
		case !ok:
			b.chanop("MODE", run.channel, "-l")
		case run.moderate && !g.Moderate:
			b.chanop("MODE", run.channel, "-m")
		}
		if ok && !forced {
			prev[key] = run
		}
	}
	for _, g := range want {
		key := ircfold.Nick(g.Channel)
		if b.guards[key] != nil {
			continue
		}
		if g.Moderate {
			b.chanop("MODE", g.Channel, "+m")
		}
		run := &guardRun{channel: g.Channel, step: g.Joins, moderate: g.Moderate, interval: g.Interval,
			minLimit: g.MinLimit, tune: tune, stop: make(chan struct{})}
		if p := prev[key]; p != nil {
			run.cur, run.graceUntil, run.highSince = p.cur, p.graceUntil, p.highSince
		} else if key == forceKey && tune.GraceSeconds > 0 {
			run.graceUntil = now.Add(time.Duration(tune.GraceSeconds) * time.Second)
			b.chanop("MODE", g.Channel, "-l")
			log.Printf("irc[%s]: guard %s -l: (re)joined, no limit for %ds", b.name, g.Channel, tune.GraceSeconds)
		}
		b.applyGuardLimit(run, now)
		if b.guards == nil {
			b.guards = map[string]*guardRun{}
		}
		b.guards[key] = run
		every := guardInterval
		if g.Interval > 0 {
			every = time.Duration(g.Interval) * time.Second
		}
		guard.Go("guard limit", func() { b.runGuardLimit(key, run, every) })
	}
}

// runGuardLimit re-checks +l every interval until its guard is stopped or the network quits.
// The check and the MODE line happen under guardMu, so no +l can follow a stop's -l.
func (b *ircNetwork) runGuardLimit(key string, run *guardRun, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-run.stop:
			return
		case <-b.quit:
			return
		case <-t.C:
		}
		b.guardMu.Lock()
		if b.guards[key] != run {
			b.guardMu.Unlock()
			return
		}
		b.applyGuardLimit(run, time.Now())
		b.guardMu.Unlock()
	}
}

// applyGuardLimit sends the +l guardRun.next decides and logs old -> new and why. With no member
// count (not joined / NAMES not seen yet) next leaves the limit alone: a guess could lock
// everyone out. Caller holds guardMu.
func (b *ircNetwork) applyGuardLimit(run *guardRun, now time.Time) {
	to, why := run.next(b.memberCount(run.channel), now)
	if why == "" || to == run.cur {
		return
	}
	log.Printf("irc[%s]: guard %s +l %d -> %d (%s)", b.name, run.channel, run.cur, to, why)
	b.chanop("MODE", run.channel, "+l", strconv.Itoa(to))
	run.cur = to
}

func (b *ircNetwork) memberCount(channel string) int {
	b.membersMu.RLock()
	defer b.membersMu.RUnlock()
	for name, members := range b.channelMembers {
		if strings.EqualFold(name, channel) {
			return len(members)
		}
	}
	return 0
}

// addNames records a 353 RPL_NAMREPLY list ("@op +voice nick" or, with userhost-in-names,
// "@nick!user@host"), dropping status prefixes.
func (b *ircNetwork) addNames(channel, names string) {
	b.membersMu.Lock()
	defer b.membersMu.Unlock()
	if b.channelMembers[channel] == nil {
		b.channelMembers[channel] = make(map[string]struct{})
	}
	for _, n := range strings.Fields(names) {
		nick, _, _ := strings.Cut(strings.TrimLeft(n, "~&@%+!"), "!")
		if nick != "" {
			b.channelMembers[channel][nick] = struct{}{}
		}
	}
}
