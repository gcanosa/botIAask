package irc

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ergochat/irc-go/ircmsg"

	"botIAask/config"
)

// whoisHostTimeout bounds how long !akick add <nick> waits for RPL_WHOISUSER (311).
const whoisHostTimeout = 5 * time.Second

// applyAccess runs on every JOIN: the first matching entry for the channel wins, with kick
// checked before op and voice. Admins are never auto-kicked (a typo'd mask must not lock
// them out). The bot needs ops in the channel; without them the server just refuses (482).
func (b *ircNetwork) applyAccess(channel, source string) {
	nick, _, _ := strings.Cut(source, "!")
	if nick == "" || b.isSelf(nick) {
		return
	}
	var op, voice bool
	for _, e := range b.netCfg().Access {
		if !strings.EqualFold(e.Channel, channel) || !config.MaskMatch(e.Mask, source) {
			continue
		}
		switch e.Level {
		case config.AccessKick:
			if b.IsAdmin(source) {
				continue
			}
			b.chanop("MODE", channel, "+b", e.Mask)
			b.chanop("KICK", channel, nick, akickReason(e.Reason))
			return
		case config.AccessOp:
			op = true
		case config.AccessVoice:
			voice = true
		}
	}
	if op {
		b.chanop("MODE", channel, "+o", nick)
	} else if voice {
		b.chanop("MODE", channel, "+v", nick)
	}
}

// chanop sends a paced MODE/KICK line (ircevent has no flood control); tests capture it via testSend.
func (b *ircNetwork) chanop(command, channel string, params ...string) {
	b.pace.wait()
	if b.testSend != nil {
		b.testSend(command, channel, strings.Join(params, " "))
		return
	}
	b.conn.Send(command, append([]string{channel}, params...)...)
}

func akickReason(r string) string {
	if r == "" {
		return "Banned"
	}
	return r
}

// channelArg takes a leading #channel from args, else falls back to target when the command
// was said in a channel. ok is false when no channel can be determined (PM without #chan).
func channelArg(target string, args []string) (channel string, rest []string, ok bool) {
	if len(args) > 0 && ircChannelTarget(args[0]) {
		return args[0], args[1:], true
	}
	return target, args, ircChannelTarget(target)
}

// handleKickCommand: !kick [#chan] <nick> [reason] (admin session required, see bot.go).
func (b *ircNetwork) handleKickCommand(target, sender string, args []string) {
	channel, rest, ok := channelArg(target, args)
	if !ok || len(rest) == 0 {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %skick [#chan] <nick> [reason]", b.pfx()))
		return
	}
	nick := rest[0]
	if b.isSelf(nick) {
		b.sendPrivmsg(target, "I won't kick myself.")
		return
	}
	reason := strings.Join(rest[1:], " ")
	if reason == "" {
		reason = "Requested by " + sender
	}
	b.chanop("KICK", channel, nick, reason)
	if !ircChannelTarget(target) {
		b.sendPrivmsg(target, fmt.Sprintf("Kicked %s from %s.", nick, channel))
	}
}

// accessCmds maps each list command to its level and the label used in replies.
var accessCmds = map[string]struct{ level, label string }{
	"akick":  {config.AccessKick, "auto-kick"},
	"aop":    {config.AccessOp, "auto-op"},
	"avoice": {config.AccessVoice, "auto-voice"},
}

// handleAccessCommand: !akick / !aop / !avoice add|del|list (admin session required, see bot.go).
func (b *ircNetwork) handleAccessCommand(target, sender, cmd string, args []string) {
	c := accessCmds[cmd]
	usage := fmt.Sprintf("Usage: %s%s add [#chan] <nick|mask> [reason] | %s%s del [#chan] <mask|number> | %s%s list [#chan]", b.pfx(), cmd, b.pfx(), cmd, b.pfx(), cmd)
	if len(args) == 0 {
		b.sendPrivmsg(target, usage)
		return
	}
	channel, rest, ok := channelArg(target, args[1:])
	if !ok {
		b.sendPrivmsg(target, usage)
		return
	}
	switch strings.ToLower(args[0]) {
	case "add":
		if len(rest) == 0 {
			b.sendPrivmsg(target, usage)
			return
		}
		b.accessAdd(target, sender, cmd, c.level, c.label, channel, rest[0], strings.Join(rest[1:], " "))
	case "del", "remove":
		if len(rest) == 0 {
			b.sendPrivmsg(target, usage)
			return
		}
		b.accessDel(target, cmd, c.level, c.label, channel, rest[0])
	case "list":
		b.accessList(target, c.level, c.label, channel)
	default:
		b.sendPrivmsg(target, usage)
	}
}

// accessEntries returns channel's entries of one level, in config order (the numbers "list" shows).
func accessEntries(list []config.ChannelAccess, level, channel string) []config.ChannelAccess {
	var out []config.ChannelAccess
	for _, e := range list {
		if e.Level == level && strings.EqualFold(e.Channel, channel) {
			out = append(out, e)
		}
	}
	return out
}

// accessAdd adds (or re-levels) a mask. A bare nick is resolved via WHOIS: kick bans the whole
// host (*!*@host, survives nick and ident changes), op/voice trust only *!user@host. The entry
// takes effect at once on the nick: kicked, opped or voiced.
func (b *ircNetwork) accessAdd(target, sender, cmd, level, label, channel, who, reason string) {
	nick := ""
	mask := who
	if !strings.ContainsAny(who, "!@*?") {
		nick = who
		if level == config.AccessKick && b.isSelf(nick) {
			b.sendPrivmsg(target, "I won't akick myself.")
			return
		}
		mask = nick + "!*@*"
		if user, host, ok := b.whoisHost(nick); ok {
			if level == config.AccessKick && b.IsAdmin(nick+"!"+user+"@"+host) {
				b.sendPrivmsg(target, fmt.Sprintf("%s is an admin; not adding.", nick))
				return
			}
			mask = "*!" + user + "@" + host
			if level == config.AccessKick {
				mask = "*!*@" + host
			}
		}
	}
	e := config.ChannelAccess{Channel: channel, Mask: mask, Level: level, Reason: reason}
	if err := config.ValidateAccess(&e); err != nil {
		b.sendPrivmsg(target, cmd+": "+err.Error())
		return
	}
	var prev string
	if err := b.editAccess("!"+cmd+" add", func(list *[]config.ChannelAccess) error {
		if i := config.AccessIndex(*list, e.Channel, e.Mask); i >= 0 {
			prev = (*list)[i].Level
			(*list)[i] = e
		} else {
			*list = append(*list, e)
		}
		return nil
	}); err != nil {
		b.sendPrivmsg(target, cmd+": "+err.Error())
		return
	}
	if prev == config.AccessKick && level != config.AccessKick {
		b.chanop("MODE", channel, "-b", e.Mask)
	}
	switch level {
	case config.AccessKick:
		b.chanop("MODE", channel, "+b", e.Mask)
		if nick != "" {
			b.chanop("KICK", channel, nick, akickReason(e.Reason))
		}
	case config.AccessOp:
		if nick != "" {
			b.chanop("MODE", channel, "+o", nick)
		}
	case config.AccessVoice:
		if nick != "" {
			b.chanop("MODE", channel, "+v", nick)
		}
	}
	b.sendPrivmsg(target, fmt.Sprintf("%s added on %s: %s (by %s).", capFirst(label), channel, e.Mask, sender))
}

func (b *ircNetwork) accessDel(target, cmd, level, label, channel, arg string) {
	mask := config.NormalizeMask(arg)
	if n, err := strconv.Atoi(arg); err == nil {
		entries := accessEntries(b.netCfg().Access, level, channel)
		if n < 1 || n > len(entries) {
			b.sendPrivmsg(target, fmt.Sprintf("No %s #%d on %s.", label, n, channel))
			return
		}
		mask = entries[n-1].Mask
	}
	err := b.editAccess("!"+cmd+" del", func(list *[]config.ChannelAccess) error {
		i := config.AccessIndex(*list, channel, mask)
		if i < 0 || (*list)[i].Level != level {
			return fmt.Errorf("%s is not on the %s %s list", mask, channel, label)
		}
		*list = append((*list)[:i], (*list)[i+1:]...)
		return nil
	})
	if err != nil {
		b.sendPrivmsg(target, cmd+": "+err.Error())
		return
	}
	if level == config.AccessKick {
		b.chanop("MODE", channel, "-b", mask)
	}
	b.sendPrivmsg(target, fmt.Sprintf("%s removed on %s: %s.", capFirst(label), channel, mask))
}

func (b *ircNetwork) accessList(target, level, label, channel string) {
	entries := accessEntries(b.netCfg().Access, level, channel)
	if len(entries) == 0 {
		b.sendPrivmsg(target, fmt.Sprintf("No %s entries on %s.", label, channel))
		return
	}
	for i, e := range entries {
		if i == maxListLines {
			b.sendPrivmsg(target, fmt.Sprintf("… and %d more (see the dashboard).", len(entries)-i))
			break
		}
		line := fmt.Sprintf("%d. %s", i+1, e.Mask)
		if e.Reason != "" {
			line += " — " + e.Reason
		}
		b.sendPrivmsg(target, b.sanitize(line))
	}
}

func capFirst(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// editAccess applies fn to this network's access list on a config clone, saves it and rehashes
// (same flow as !gh add/del).
func (b *ircNetwork) editAccess(source string, fn func(list *[]config.ChannelAccess) error) error {
	clone, err := config.CloneConfig(b.getCfg())
	if err != nil {
		return err
	}
	i := config.IRCNetworkIndex(clone.IRC.Networks, b.name)
	if i < 0 {
		return fmt.Errorf("network %s not in config", b.name)
	}
	if err := fn(&clone.IRC.Networks[i].Access); err != nil {
		return err
	}
	if err := config.SaveConfig(configPathOrDefault(b.Bot), clone); err != nil {
		return err
	}
	if err := b.RunRehash(source); err != nil {
		return fmt.Errorf("saved, but rehash failed: %w", err)
	}
	return nil
}

// whoisHost asks the server for nick's user and host (RPL_WHOISUSER 311); ok is false if the
// nick is offline (401/318 without 311) or the server doesn't answer within whoisHostTimeout.
func (b *ircNetwork) whoisHost(nick string) (user, host string, ok bool) {
	type res struct{ user, host string }
	ch := make(chan *res, 1)
	var once sync.Once
	send := func(r *res) { once.Do(func() { ch <- r }) }
	match := func(e ircmsg.Message) bool { return len(e.Params) >= 2 && strings.EqualFold(e.Params[1], nick) }

	userID := b.conn.AddCallback("311", func(e ircmsg.Message) {
		if match(e) && len(e.Params) >= 4 {
			send(&res{e.Params[2], e.Params[3]})
		}
	})
	endID := b.conn.AddCallback("318", func(e ircmsg.Message) {
		if match(e) {
			send(nil)
		}
	})
	noID := b.conn.AddCallback("401", func(e ircmsg.Message) {
		if match(e) {
			send(nil)
		}
	})
	defer b.conn.RemoveCallback(userID)
	defer b.conn.RemoveCallback(endID)
	defer b.conn.RemoveCallback(noID)

	if err := b.conn.Send("WHOIS", nick); err != nil {
		return "", "", false
	}
	select {
	case r := <-ch:
		if r == nil {
			return "", "", false
		}
		return r.user, r.host, true
	case <-time.After(whoisHostTimeout):
		return "", "", false
	}
}

// EnforceAccessBan sets (add) or lifts the channel ban for an auto-kick mask on a live network;
// the dashboard calls it after editing the list. No-op when the network isn't connected.
func (b *Bot) EnforceAccessBan(network, channel, mask string, add bool) {
	n := b.network(network)
	if n == nil || !n.isConnected() {
		return
	}
	flag := "-b"
	if add {
		flag = "+b"
	}
	n.chanop("MODE", channel, flag, mask)
}
