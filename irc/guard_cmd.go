package irc

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"botIAask/internal/guard"
	"botIAask/internal/ircfold"
)

const (
	guardDefaultStep = 5  // joins allowed per guardInterval while guarded
	guardMaxStep     = 50 // larger than this isn't a guard any more
)

// guardInterval is how often a guarded channel's +l is re-based on its member count.
// ponytail: fixed 30s, make it an argument if admins ever need a different pace.
var guardInterval = 30 * time.Second

// handleGuardCommand: !guard [#chan] on [joins] | off (admin session required, see bot.go).
// on: +m (only +v and up can talk), voices the caller in the same line (harmless when they're
// already +v/+h/+o/+q), and keeps +l at members+joins, re-based every guardInterval, so at most
// that many people get in per interval: the limit follows the channel up and down instead of
// closing it. off: -ml. Needs ops; the server answers 482 otherwise (logged in network.go).
func (b *ircNetwork) handleGuardCommand(target, sender string, args []string) {
	channel, rest, ok := channelArg(target, args)
	step := guardDefaultStep
	valid := ok && len(rest) >= 1 && len(rest) <= 2
	if valid && len(rest) == 2 {
		n, err := strconv.Atoi(rest[1])
		step, valid = n, err == nil && n >= 1 && n <= guardMaxStep && strings.EqualFold(rest[0], "on")
	}
	on := valid && strings.EqualFold(rest[0], "on")
	if !valid || (!on && !strings.EqualFold(rest[0], "off")) {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sguard [#chan] on [joins 1-%d, default %d] | off", b.pfx(), guardMaxStep, guardDefaultStep))
		return
	}
	key := ircfold.Nick(channel)
	b.guardMu.Lock()
	if stop, running := b.guards[key]; running {
		close(stop)
		delete(b.guards, key)
	}
	var reply string
	if on {
		stop := make(chan struct{})
		if b.guards == nil {
			b.guards = map[string]chan struct{}{}
		}
		b.guards[key] = stop
		b.chanop("MODE", channel, "+mv", sender)
		last := b.setGuardLimit(channel, step, 0)
		every := guardInterval
		guard.Go("guard limit", func() { b.runGuardLimit(channel, key, step, last, every, stop) })
		reply = fmt.Sprintf("Guard on for %s: moderated, at most %d joins every %s.", channel, step, every)
	} else {
		b.chanop("MODE", channel, "-ml")
		reply = fmt.Sprintf("Guard off for %s.", channel)
	}
	b.guardMu.Unlock()
	b.sendPrivmsg(target, reply)
}

// runGuardLimit re-bases +l every guardInterval until stopped (by !guard off/on) or the network quits.
// The check and the MODE line happen under guardMu, so no +l can follow !guard off's -ml.
func (b *ircNetwork) runGuardLimit(channel, key string, step, last int, every time.Duration, stop chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-b.quit:
			return
		case <-t.C:
		}
		b.guardMu.Lock()
		if b.guards[key] != stop {
			b.guardMu.Unlock()
			return
		}
		last = b.setGuardLimit(channel, step, last)
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
