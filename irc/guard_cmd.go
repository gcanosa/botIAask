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
type guardRun struct {
	channel  string
	step     int
	moderate bool
	interval int // seconds, 0 = guardInterval
	stop     chan struct{}
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
// then +l members+joins). force (a channel, or "") restarts that channel's guard even when
// unchanged, re-sending its modes: used after the bot (re)joins it (366 RPL_ENDOFNAMES).
func (b *ircNetwork) syncGuards(force string) {
	want, err := config.NormalizeGuard(b.netCfg().Guard)
	if err != nil {
		log.Printf("irc[%s]: guard config ignored: %v", b.name, err)
	}
	find := func(key string) (config.ChannelGuard, bool) {
		for _, g := range want {
			if ircfold.Nick(g.Channel) == key {
				return g, true
			}
		}
		return config.ChannelGuard{}, false
	}
	forceKey := ircfold.Nick(force)
	b.guardMu.Lock()
	defer b.guardMu.Unlock()
	for key, run := range b.guards {
		g, ok := find(key)
		if ok && g.Joins == run.step && g.Moderate == run.moderate && g.Interval == run.interval && (force == "" || key != forceKey) {
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
	}
	for _, g := range want {
		key := ircfold.Nick(g.Channel)
		if b.guards[key] != nil {
			continue
		}
		if g.Moderate {
			b.chanop("MODE", g.Channel, "+m")
		}
		last := b.setGuardLimit(g.Channel, g.Joins, 0)
		run := &guardRun{channel: g.Channel, step: g.Joins, moderate: g.Moderate, interval: g.Interval, stop: make(chan struct{})}
		if b.guards == nil {
			b.guards = map[string]*guardRun{}
		}
		b.guards[key] = run
		every := guardInterval
		if g.Interval > 0 {
			every = time.Duration(g.Interval) * time.Second
		}
		guard.Go("guard limit", func() { b.runGuardLimit(key, run, last, every) })
	}
}

// runGuardLimit re-bases +l every interval until its guard is stopped or the network quits.
// The check and the MODE line happen under guardMu, so no +l can follow a stop's -l.
func (b *ircNetwork) runGuardLimit(key string, run *guardRun, last int, every time.Duration) {
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
		last = b.setGuardLimit(run.channel, run.step, last)
		b.guardMu.Unlock()
	}
}

// setGuardLimit sends +l members+step when it differs from last; returns the limit now in force.
// With no member count (not joined / NAMES not seen yet) it leaves the limit alone: a guess
// could lock everyone out.
func (b *ircNetwork) setGuardLimit(channel string, step, last int) int {
	count := b.memberCount(channel)
	if count == 0 || count+step == last {
		return last
	}
	b.chanop("MODE", channel, "+l", strconv.Itoa(count+step))
	return count + step
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
