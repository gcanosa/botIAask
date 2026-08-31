package irc

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"botIAask/bookmarks"
	"botIAask/internal/guard"
)

// --- pending !tell cache (avoids a DB hit on every channel line) ---

// hasPendingTell/markPendingTell/clearPendingTell/tellPending are keyed by
// adminSessionKey(network, nick) so a nick's pending-tell cache doesn't bleed across
// networks (the underlying tells are stored per-network too, see bookmarks.Tell).
func (b *Bot) hasPendingTell(network, nick string) bool {
	b.tellMu.RLock()
	defer b.tellMu.RUnlock()
	_, ok := b.tellPending[adminSessionKey(network, nick)]
	return ok
}

func (b *Bot) markPendingTell(network, nick string) {
	b.tellMu.Lock()
	b.tellPending[adminSessionKey(network, nick)] = struct{}{}
	b.tellMu.Unlock()
}

func (b *Bot) clearPendingTell(network, nick string) {
	b.tellMu.Lock()
	delete(b.tellPending, adminSessionKey(network, nick))
	b.tellMu.Unlock()
}

// loadPendingTells primes the pending-tell cache for every configured network at
// startup (called once from Bot.Start(), before any network may be connected yet).
func (b *Bot) loadPendingTells() {
	if b.bookmarksDB == nil {
		return
	}
	b.tellMu.Lock()
	defer b.tellMu.Unlock()
	for _, netCfg := range b.getCfg().IRC.Networks {
		folds, err := b.bookmarksDB.PendingTellFolds(netCfg.Name)
		if err != nil {
			log.Printf("tell: load pending for %s: %v", netCfg.Name, err)
			continue
		}
		for _, f := range folds {
			b.tellPending[netCfg.Name+"\x00"+f] = struct{}{}
		}
	}
}

// isUserOnline reports whether nick is currently in any channel on this network.
func (b *ircNetwork) isUserOnline(nick string) bool {
	fold := bookmarks.IRCCaseFoldNick(nick)
	b.membersMu.RLock()
	defer b.membersMu.RUnlock()
	for _, members := range b.channelMembers {
		for m := range members {
			if bookmarks.IRCCaseFoldNick(m) == fold {
				return true
			}
		}
	}
	return false
}

// seenTargets returns the channel to record (empty for a PM) and the target to
// reply to (the channel for channel messages, the sender's nick for a PM).
func seenTargets(target, sender string) (channel, reply string) {
	if strings.HasPrefix(target, "#") {
		return target, target
	}
	return "", sender
}

// --- last-seen tracking ---

// recordSeen updates the last-seen record for nick; never blocks command flow on error.
// ponytail: one UPSERT per channel line; fine at IRC volume. Batch only if write load ever shows up.
func (b *ircNetwork) recordSeen(nick, channel, action, message string) {
	if b.bookmarksDB == nil || nick == "" {
		return
	}
	if bookmarks.IRCCaseFoldNick(nick) == bookmarks.IRCCaseFoldNick(b.netCfg().Nickname) {
		return // don't track the bot itself
	}
	if err := b.bookmarksDB.RecordSeen(b.name, nick, channel, action, message); err != nil && b.getCfg().Bot.Debug {
		log.Printf("[DEBUG] recordSeen: %v", err)
	}
}

// --- !tell delivery ---

// deliverTells flushes any pending !tell messages for nick (on this network) to
// replyTarget (the channel they spoke/joined in, or their nick for a PM).
func (b *ircNetwork) deliverTells(nick, replyTarget string) {
	if b.bookmarksDB == nil || !b.hasPendingTell(b.name, nick) {
		return
	}
	tells, err := b.bookmarksDB.TakeTells(b.name, nick)
	if err != nil {
		log.Printf("tell: take for %s: %v", nick, err)
		return
	}
	b.clearPendingTell(b.name, nick)
	for _, t := range tells {
		b.sendPrivmsg(replyTarget, fmt.Sprintf("%s: %s left a message %s ago: %s",
			nick, t.FromNick, humanizeSince(t.CreatedAt), t.Message))
	}
}

// --- command handlers ---

// handleTellCommand: !tell <nick> <message> — store a message delivered when <nick> is next seen.
func (b *ircNetwork) handleTellCommand(target, sender, message string) {
	if b.bookmarksDB == nil {
		b.sendPrivmsg(target, "Bookmarks database not initialized.")
		return
	}
	body := strings.TrimSpace(strings.TrimPrefix(message, b.pfx()+"tell"))
	fields := strings.SplitN(body, " ", 2)
	if len(fields) < 2 || strings.TrimSpace(fields[0]) == "" || strings.TrimSpace(fields[1]) == "" {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %stell <nick> <message>", b.pfx()))
		return
	}
	toNick := strings.TrimSpace(fields[0])
	note := strings.TrimSpace(fields[1])

	if bookmarks.IRCCaseFoldNick(toNick) == bookmarks.IRCCaseFoldNick(b.netCfg().Nickname) {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: I'm right here.", sender))
		return
	}
	if bookmarks.IRCCaseFoldNick(toNick) == bookmarks.IRCCaseFoldNick(sender) {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: Talking to yourself? Use %sreminder.", sender, b.pfx()))
		return
	}

	if _, err := b.bookmarksDB.AddTell(b.name, sender, toNick, note); err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: Error storing message: %v", sender, err))
		return
	}
	b.markPendingTell(b.name, toNick)
	b.sendPrivmsg(target, fmt.Sprintf("@%s: I'll tell %s when they're next around.", sender, toNick))
}

// handleSeenCommand: !seen <nick> — report when a nick was last observed on this network.
func (b *ircNetwork) handleSeenCommand(target, sender, message string) {
	if b.bookmarksDB == nil {
		b.sendPrivmsg(target, "Bookmarks database not initialized.")
		return
	}
	nick := strings.TrimSpace(strings.TrimPrefix(message, b.pfx()+"seen"))
	if nick == "" {
		b.sendPrivmsg(target, fmt.Sprintf("Usage: %sseen <nick>", b.pfx()))
		return
	}
	if bookmarks.IRCCaseFoldNick(nick) == bookmarks.IRCCaseFoldNick(sender) {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: You're right here.", sender))
		return
	}
	s, ok, err := b.bookmarksDB.GetSeen(b.name, nick)
	if err != nil {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: Error: %v", sender, err))
		return
	}
	if !ok {
		b.sendPrivmsg(target, fmt.Sprintf("@%s: I haven't seen %s.", sender, nick))
		return
	}
	where := ""
	if s.Channel != "" && strings.HasPrefix(s.Channel, "#") {
		where = " in " + s.Channel
	}
	detail := ""
	if s.Action == "message" && s.Message != "" {
		detail = fmt.Sprintf(`, saying: %s`, truncateReminderNotice(s.Message, 120))
	} else if s.Action != "" {
		detail = fmt.Sprintf(" (%s)", s.Action)
	}
	b.sendPrivmsg(target, fmt.Sprintf("@%s: %s was last seen %s ago%s%s.",
		sender, s.Nick, humanizeSince(s.LastSeen), where, detail))
}

// --- timed-reminder scheduler ---

// startReminderScheduler fires timed reminders that have come due, delivered on
// whichever network the reminder was created on. Runs for the life of the process;
// reuses each network's persistent connection across reconnects.
func (b *Bot) startReminderScheduler() {
	b.loadPendingTells()
	guard.Go("reminder scheduler", func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if b.bookmarksDB == nil || !b.IsConnected() {
				continue
			}
			due, err := b.bookmarksDB.DueReminders(time.Now())
			if err != nil {
				log.Printf("reminder scheduler: %v", err)
				continue
			}
			for _, r := range due {
				net := b.network(r.Network)
				if net != nil && net.isUserOnline(r.OwnerNick) {
					net.sendNotice(r.OwnerNick, fmt.Sprintf("[Reminder %s] %s", r.PublicID, truncateReminderNotice(r.Note, 380)))
					if _, err := b.bookmarksDB.DeleteReminder(r.Network, r.OwnerNick, r.PublicID); err != nil {
						log.Printf("reminder scheduler: delete %s: %v", r.PublicID, err)
					}
				} else {
					// Owner offline (or network not live): demote to on-join so it isn't lost.
					if err := b.bookmarksDB.ClearReminderDue(r.PublicID); err != nil {
						log.Printf("reminder scheduler: demote %s: %v", r.PublicID, err)
					}
				}
			}
		}
	})
}

// --- helpers ---

// parseReminderDelay parses a delay token like "0", "30s", "15m", "2h", "1h30m", "3d".
// Returns (0, true) for "0" (legacy on-join). Returns ok=false if not a duration.
func parseReminderDelay(tok string) (time.Duration, bool) {
	tok = strings.TrimSpace(strings.ToLower(tok))
	if tok == "" {
		return 0, false
	}
	if tok == "0" {
		return 0, true
	}
	if days, ok := strings.CutSuffix(tok, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, true
		}
		return 0, false
	}
	if d, err := time.ParseDuration(tok); err == nil && d > 0 {
		return d, true
	}
	return 0, false
}

// humanizeSince renders a coarse "2h", "3d", "5m" style age for the given past time.
func humanizeSince(t time.Time) string {
	return humanizeDuration(time.Since(t))
}

// humanizeDuration renders a coarse single-unit duration ("5m", "2h", "3d").
func humanizeDuration(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
