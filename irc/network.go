package irc

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/internal/guard"
	"botIAask/logger"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"
)

// ircNetwork owns one IRC server connection and its per-connection runtime state.
// It embeds *Bot so every global Bot method (getCfg, pfx, cmd, sanitize, IsAdmin, the
// DB handles, ...) is promoted for free; only connection-scoped behavior lives here.
type ircNetwork struct {
	*Bot
	name string

	conn *ircevent.Connection

	// pendingPings: in-flight CTCP PING round-trips keyed by folded nick (see ping_cmd.go).
	pingMu       sync.Mutex
	pendingPings map[string]pendingPing

	// statsMu guards connected/connectionTime (shadows nothing on Bot; Bot's own statsMu
	// guards the unrelated global aiRequests counter).
	statsMu        sync.Mutex
	connected      bool
	connectionTime time.Time

	// Channel membership tracking for this network: channel -> set of users.
	channelMembers map[string]map[string]struct{}
	membersMu      sync.RWMutex

	// sessionJoins: runtime-only JOINs on this network (not in config; lost on new
	// process, rejoined on IRC reconnect in-process).
	sessionJoins   []config.IRChannel
	sessionJoinsMu sync.Mutex

	authenticated bool
	authMu        sync.RWMutex

	// quit is closed by disconnectNetwork to cancel this network's initial-connect retry
	// loop (connectWithRetry) while it's still backing off and not yet live/registered in
	// b.networks — without this, removing/disabling an unreachable network during backoff
	// doesn't stop the retry, and it can re-register itself once the server comes back
	// despite being gone from config. quitOnce guards against a double close.
	quit     chan struct{}
	quitOnce sync.Once
}

// requestQuit closes n.quit exactly once (safe to call more than once or concurrently).
func (n *ircNetwork) requestQuit() {
	n.quitOnce.Do(func() { close(n.quit) })
}

// cancelled reports whether requestQuit has been called on this network.
func (n *ircNetwork) cancelled() bool {
	select {
	case <-n.quit:
		return true
	default:
		return false
	}
}

// netCfg reads this network's live config entry (fresh on every rehash).
func (b *ircNetwork) netCfg() config.IRCNetworkConfig {
	nc, _ := config.FindIRCNetworkByName(b.getCfg().IRC.Networks, b.name)
	return nc
}

func (b *ircNetwork) isConnected() bool {
	b.statsMu.Lock()
	defer b.statsMu.Unlock()
	return b.connected
}

// IsAuthenticated returns whether this network is SASL-authenticated with
// services. Shadows the promoted Bot.IsAuthenticated (embedded via *Bot) —
// without this method, net.IsAuthenticated() resolves to Bot's method, which
// loops over networks calling net.IsAuthenticated() again: infinite recursion.
func (b *ircNetwork) IsAuthenticated() bool {
	b.authMu.RLock()
	defer b.authMu.RUnlock()
	return b.authenticated
}

// IsAdmin shadows the promoted Bot.IsAdmin: admin on this network means matching the
// global admin.admins list (bot-wide) OR this network's own admins list (network-only,
// config.IRCNetworkConfig.Admins).
func (b *ircNetwork) IsAdmin(fullHostmask string) bool {
	if b.Bot.IsAdmin(fullHostmask) {
		return true
	}
	for _, admin := range b.netCfg().Admins {
		if strings.Contains(fullHostmask, admin) {
			return true
		}
	}
	return false
}

// adminSessionKey builds the composite loggedInAdmins key so an admin session on one
// network doesn't leak admin rights on another (nicks aren't unique across networks).
func adminSessionKey(network, nick string) string {
	return network + "\x00" + bookmarks.IRCCaseFoldNick(nick)
}

// splitAdminSessionKey reverses adminSessionKey, returning the network name and the
// (already case-folded) nick.
func splitAdminSessionKey(key string) (network, foldedNick string, ok bool) {
	i := strings.IndexByte(key, 0)
	if i < 0 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// network looks up a live (connected or connecting) network by name.
func (b *Bot) network(name string) *ircNetwork {
	b.networksMu.RLock()
	defer b.networksMu.RUnlock()
	return b.networks[name]
}

// networkOrDefault falls back to the first configured network when name is empty
// (back-compat for callers/rows that predate multi-network support).
func (b *Bot) networkOrDefault(name string) *ircNetwork {
	if name != "" {
		return b.network(name)
	}
	nets := b.getCfg().IRC.Networks
	if len(nets) == 0 {
		return nil
	}
	return b.network(nets[0].Name)
}

// networksSnapshot returns a stable slice of currently live networks.
func (b *Bot) networksSnapshot() []*ircNetwork {
	b.networksMu.RLock()
	defer b.networksMu.RUnlock()
	out := make([]*ircNetwork, 0, len(b.networks))
	for _, n := range b.networks {
		out = append(out, n)
	}
	return out
}

// NetworkStatus is a snapshot of one configured network's live connection state.
type NetworkStatus struct {
	Name          string `json:"name"`
	Server        string `json:"server"`
	Connected     bool   `json:"connected"`
	Authenticated bool   `json:"authenticated"`
	Nickname      string `json:"nickname"`
	ChannelCount  int    `json:"channel_count"`
}

// NetworkStatuses returns one row per configured network (including any not yet
// connected), for the web dashboard.
func (b *Bot) NetworkStatuses() []NetworkStatus {
	cfgNets := b.getCfg().IRC.Networks
	out := make([]NetworkStatus, 0, len(cfgNets))
	for _, nc := range cfgNets {
		net := b.network(nc.Name)
		st := NetworkStatus{
			Name:         nc.Name,
			Server:       fmt.Sprintf("%s:%d", nc.Server, nc.Port),
			Nickname:     nc.Nickname,
			ChannelCount: len(nc.Channels),
		}
		if net != nil {
			st.Connected = net.isConnected()
			st.Authenticated = net.IsAuthenticated()
		}
		out = append(out, st)
	}
	return out
}

// Start launches one goroutine per configured network and blocks until all have
// exited. Each network's ircevent.Loop() handles its own reconnect-with-backoff
// internally once connected; the initial Connect() retries with capped backoff here.
func (b *Bot) Start() error {
	// Fire timed reminders and prime the pending-tell cache for the process lifetime.
	// Global (not per-network): reminders/tells are delivered on whichever network the
	// owner is currently seen on.
	b.startReminderScheduler()

	nets := b.getCfg().IRC.Networks
	var wg sync.WaitGroup
	for _, netCfg := range nets {
		if !netCfg.IsEnabled() {
			log.Printf("irc[%s]: enabled: false, skipping connect", netCfg.Name)
			continue
		}
		netCfg := netCfg
		wg.Add(1)
		guard.Go("irc:"+netCfg.Name, func() {
			defer wg.Done()
			b.runNetwork(netCfg)
		})
	}
	wg.Wait()
	return nil
}

// runNetwork builds one network, registers it as pending while its initial connect retries,
// promotes it to live in b.networks on success, blocks in its event loop, and unregisters it
// when the loop exits (Quit() or a fatal disconnect). A removal/disable requested while still
// pending (disconnectNetwork closing n.quit) cancels the retry instead of leaking a goroutine
// that would otherwise re-register itself once the server comes back.
func (b *Bot) runNetwork(netCfg config.IRCNetworkConfig) {
	n := b.buildNetwork(netCfg)

	b.pendingMu.Lock()
	b.pending[netCfg.Name] = n
	b.pendingMu.Unlock()

	err := b.connectWithRetry(n)

	b.pendingMu.Lock()
	if b.pending[netCfg.Name] == n {
		delete(b.pending, netCfg.Name)
	}
	b.pendingMu.Unlock()

	if err != nil {
		if err == errNetworkCancelled {
			log.Printf("irc[%s]: connect cancelled (network removed/disabled while retrying)", netCfg.Name)
		} else {
			log.Printf("irc[%s]: connect failed permanently: %v", netCfg.Name, err)
		}
		return
	}

	// A cancellation can race a just-completed connect (disconnectNetwork's pending lookup
	// and this goroutine's delete above can interleave); check once more before going live.
	if n.cancelled() {
		n.conn.QuitMessage = n.FormatQuitMessage("")
		n.conn.Quit()
		return
	}

	b.networksMu.Lock()
	b.networks[netCfg.Name] = n
	b.networksMu.Unlock()

	n.conn.Loop() // blocks; returns when Quit()/a fatal error tears the connection down

	b.networksMu.Lock()
	// Compare-and-delete: an endpoint-change rehash (ApplyLiveConfig) may have already
	// disconnected this instance and spawned its replacement under the same name before
	// this goroutine's Loop() unwound. Only remove the entry if it's still this instance,
	// so a stale goroutine can never erase a live replacement's registration.
	if b.networks[netCfg.Name] == n {
		delete(b.networks, netCfg.Name)
	}
	b.networksMu.Unlock()
}

// errNetworkCancelled is returned by connectWithRetry when n.quit is closed mid-backoff.
var errNetworkCancelled = errors.New("network connect cancelled")

// connectWithRetry retries n.conn.Connect() with capped exponential backoff (same retry
// policy the bot has always used for its single connection), stopping early if n.quit is
// closed. ircevent only enters its own reconnect path after Loop() runs; a failed first
// Connect() returns here and never reaches Loop(), so this is a daemon — retry forever
// rather than give up and leave idle.
func (b *Bot) connectWithRetry(n *ircNetwork) error {
	backoff := 2 * time.Second
	const maxBackoff = 2 * time.Minute
	for attempt := 1; ; attempt++ {
		if n.cancelled() {
			return errNetworkCancelled
		}
		if err := n.conn.Connect(); err == nil {
			return nil
		} else {
			log.Printf("irc[%s]: connect attempt %d failed (retrying in %s): %v", n.name, attempt, backoff, err)
		}
		select {
		case <-time.After(backoff):
		case <-n.quit:
			return errNetworkCancelled
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// buildNetwork constructs one ircNetwork and registers its event callbacks, without
// connecting (see connectWithRetry). Callback bodies are the same as before multi-network
// support, scoped to this network via the *ircNetwork receiver.
func (b *Bot) buildNetwork(netCfg config.IRCNetworkConfig) *ircNetwork {
	n := &ircNetwork{Bot: b, name: netCfg.Name, channelMembers: make(map[string]map[string]struct{}), quit: make(chan struct{})}

	serverAddr := fmt.Sprintf("%s:%d", netCfg.Server, netCfg.Port)
	n.conn = &ircevent.Connection{
		Server:        serverAddr,
		Nick:          netCfg.Nickname,
		User:          netCfg.Nickname,
		RealName:      netCfg.Nickname,
		UseTLS:        netCfg.UseSSL,
		Debug:         b.getCfg().Bot.Debug,
		RequestCaps:   []string{"server-time", "message-tags", "sasl"},
		ReconnectFreq: 30 * time.Second,
		KeepAlive:     60 * time.Second,
		Timeout:       30 * time.Second,
	}

	if netCfg.UseSSL && netCfg.TLSSkipVerify {
		n.conn.TLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in, e.g. bare-IP servers with no matching SANs
	}

	if netCfg.Services.Enabled {
		n.conn.SASLLogin = netCfg.Services.Username
		n.conn.SASLPassword = netCfg.Services.Password
		if b.getCfg().Bot.Debug {
			log.Printf("[DEBUG] irc[%s]: SASL Authentication enabled for user: %s", n.name, n.conn.SASLLogin)
		}
	}

	authSuccess := func(e ircmsg.Message) {
		n.authMu.Lock()
		n.authenticated = true
		n.authMu.Unlock()
		if b.getCfg().Bot.Debug {
			log.Printf("[DEBUG] irc[%s]: Successfully authenticated with services.", n.name)
		}
	}
	n.conn.AddCallback("900", authSuccess)
	n.conn.AddCallback("903", authSuccess)

	authFail := func(e ircmsg.Message) {
		n.authMu.Lock()
		n.authenticated = false
		n.authMu.Unlock()
		log.Printf("[ERROR] irc[%s]: Authentication failed: %s", n.name, e.Params[len(e.Params)-1])
	}
	n.conn.AddCallback("902", authFail)
	n.conn.AddCallback("904", authFail)
	n.conn.AddCallback("905", authFail)

	n.conn.AddConnectCallback(func(e ircmsg.Message) {
		log.Printf("irc[%s]: connected to %s! Joining channels...", n.name, serverAddr)
		n.statsMu.Lock()
		n.connectionTime = time.Now()
		n.connected = true
		n.statsMu.Unlock()
		for _, channel := range n.netCfg().Channels {
			if !channel.AutoJoinEnabled() {
				if b.getCfg().Bot.Debug {
					log.Printf("[DEBUG] irc[%s]: skipping auto-join (auto_join: false): %s", n.name, channel.Name)
				}
				continue
			}
			if b.getCfg().Bot.Debug {
				if channel.Password != "" {
					log.Printf("[DEBUG] irc[%s]: joining channel: %s (key set)", n.name, channel.Name)
				} else {
					log.Printf("[DEBUG] irc[%s]: joining channel: %s", n.name, channel.Name)
				}
			}
			if err := ircJoinWithKey(n.conn, channel); err != nil {
				log.Printf("irc[%s]: join %s: %v", n.name, channel.Name, err)
			}
		}
		n.rejoinSessionChannels()
	})

	n.conn.AddCallback("PRIVMSG", func(e ircmsg.Message) {
		target := e.Params[0] // Channel or Nick
		message := e.Params[1]
		sender := e.Nick()

		if b.getCfg().Bot.Debug {
			log.Printf("[DEBUG] irc[%s]: PRIVMSG received - Sender: %s, Target: %s, Content: %s", n.name, sender, target, message)
		}

		// A pending "!gh add --private" token reply must never reach logs/, !seen, or
		// !tell — checked and consumed here, before any of that, rather than inside
		// dispatchCommand (which runs after the normal logging call below).
		if n.tryConsumePendingGitHubToken(target, message, sender) {
			return
		}

		if strings.HasPrefix(message, "\x01") && strings.HasSuffix(message, "\x01") {
			ctcpContent := message[1 : len(message)-1]
			if strings.HasPrefix(ctcpContent, "ACTION ") {
				actionMsg := ctcpContent[7:]
				logger.LogChannelEvent(n.name, target, logger.EventAction, sender, actionMsg, "")
				if b.tracker != nil {
					b.tracker.LogAction(n.name, sender)
				}
				ch, reply := seenTargets(target, sender)
				n.recordSeen(sender, ch, "action", actionMsg)
				n.deliverTells(sender, reply)
			} else {
				n.handleCTCPRequest(sender, target, ctcpContent)
			}
		} else {
			logger.LogChannelEvent(n.name, target, logger.EventMessage, sender, message, "")
			if b.tracker != nil {
				b.tracker.LogMessage(n.name, sender)
			}
			ch, reply := seenTargets(target, sender)
			n.recordSeen(sender, ch, "message", message)
			n.dispatchCommand(target, message, sender, e.Source)
			n.deliverTells(sender, reply)
		}
	})

	n.conn.AddCallback("NOTICE", func(e ircmsg.Message) {
		if len(e.Params) < 2 {
			return
		}
		target := e.Params[0]
		message := e.Params[1]
		sender := e.Nick()
		if n.handlePingReply(sender, message) {
			return
		}
		logger.LogChannelEvent(n.name, target, logger.EventNotice, sender, message, "")
	})

	n.conn.AddCallback("JOIN", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		target := e.Params[0] // Channel
		sender := e.Nick()
		logger.LogChannelEvent(n.name, target, logger.EventJoin, sender, "", "")

		n.membersMu.Lock()
		if _, exists := n.channelMembers[target]; !exists {
			n.channelMembers[target] = make(map[string]struct{})
		}
		n.channelMembers[target][sender] = struct{}{}
		n.membersMu.Unlock()

		if b.tracker != nil {
			b.tracker.LogJoin(n.name)
			n.updateTrackerAdmins()
		}

		n.recordSeen(sender, target, "join", "")

		if b.bookmarksDB != nil && bookmarks.IRCCaseFoldNick(sender) != bookmarks.IRCCaseFoldNick(n.netCfg().Nickname) {
			rems, err := b.bookmarksDB.ListJoinReminders(n.name, sender)
			if err != nil {
				if b.getCfg().Bot.Debug {
					log.Printf("[DEBUG] ListJoinReminders on JOIN: %v", err)
				}
			} else {
				const maxJoinNoteBytes = 380
				for _, r := range rems {
					note := truncateReminderNotice(r.Note, maxJoinNoteBytes)
					n.sendNotice(sender, fmt.Sprintf("[Reminder %s] %s", r.PublicID, note))
				}
			}
			n.deliverTells(sender, target)
		}
	})

	n.conn.AddCallback("PART", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		target := e.Params[0] // Channel
		sender := e.Nick()
		message := ""
		if len(e.Params) > 1 {
			message = e.Params[1]
		}
		logger.LogChannelEvent(n.name, target, logger.EventPart, sender, message, "")

		n.membersMu.Lock()
		if members, exists := n.channelMembers[target]; exists {
			delete(members, sender)
		}
		n.membersMu.Unlock()

		if b.tracker != nil {
			b.tracker.LogPart(n.name)
			n.updateTrackerAdmins()
		}

		n.recordSeen(sender, target, "part", message)
	})

	n.conn.AddCallback("KICK", func(e ircmsg.Message) {
		if len(e.Params) < 2 {
			return
		}
		target := e.Params[0] // Channel
		kicked := e.Params[1]
		sender := e.Nick()
		message := ""
		if len(e.Params) > 2 {
			message = e.Params[2]
		}
		logger.LogChannelEvent(n.name, target, logger.EventKick, sender, message, kicked)

		n.membersMu.Lock()
		if members, exists := n.channelMembers[target]; exists {
			delete(members, kicked)
		}
		n.membersMu.Unlock()

		if b.tracker != nil {
			n.updateTrackerAdmins()
		}
	})

	// QUIT and NICK are not channel-specific, we'll log them globally or skip.
	n.conn.AddCallback("QUIT", func(e ircmsg.Message) {
		sender := e.Nick()
		message := ""
		if len(e.Params) > 0 {
			message = e.Params[0]
		}
		// For quits, we log to all configured channels as we might not have a full state tracker
		for _, channel := range n.netCfg().Channels {
			logger.LogChannelEvent(n.name, channel.Name, logger.EventQuit, sender, message, "")
		}

		n.membersMu.Lock()
		for _, members := range n.channelMembers {
			delete(members, sender)
		}
		n.membersMu.Unlock()

		if b.tracker != nil {
			b.tracker.LogPart(n.name)
			n.updateTrackerAdmins()
		}

		n.recordSeen(sender, "", "quit", message)
	})

	n.conn.AddCallback("NICK", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		sender := e.Nick()
		newNick := e.Params[0]
		for _, channel := range n.netCfg().Channels {
			logger.LogChannelEvent(n.name, channel.Name, logger.EventNick, sender, newNick, "")
		}

		n.membersMu.Lock()
		for _, members := range n.channelMembers {
			if _, exists := members[sender]; exists {
				delete(members, sender)
				members[newNick] = struct{}{}
			}
		}
		n.membersMu.Unlock()

		b.loginsMu.Lock()
		oldKey := adminSessionKey(n.name, sender)
		if b.loggedInAdmins[oldKey] {
			delete(b.loggedInAdmins, oldKey)
			b.loggedInAdmins[adminSessionKey(n.name, newNick)] = true
		}
		b.loginsMu.Unlock()

		if b.tracker != nil {
			n.updateTrackerAdmins()
		}
	})

	n.conn.AddDisconnectCallback(func(e ircmsg.Message) {
		n.statsMu.Lock()
		n.connected = false
		n.statsMu.Unlock()
		// Also clear authenticated: left true across a disconnect, it made
		// Bot.IsAuthenticated() (an OR across networks) report stale SASL success for a
		// network that's no longer even connected.
		n.authMu.Lock()
		n.authenticated = false
		n.authMu.Unlock()
		if b.getCfg().Bot.Debug {
			log.Printf("irc[%s]: disconnected from IRC server", n.name)
		}
	})

	return n
}

// ApplyLiveConfig swaps in a new config, then reconciles the live network set against
// newCfg.IRC.Networks: added networks are connected, removed ones are torn down,
// endpoint changes (server/port/nick/TLS/SASL) reconnect just that one network, and
// channel-list-only changes are hot join/part'd on the existing connection. No full
// bot restart is required for any network add/remove/edit.
func (b *Bot) ApplyLiveConfig(newCfg *config.Config) {
	oldCfg := b.cfg.Load()
	b.cfg.Store(newCfg)

	// Re-prime tellPending for the new network set: loadPendingTells only ever ran once
	// from Start(), so a network added at runtime (rather than present at process start)
	// started with an empty pending-tell cache and silently never delivered its
	// already-queued DB tells. Safe to call again — it only adds keys that DueReminders/
	// TakeTells' DB state still says are pending, never removes.
	b.loadPendingTells()

	b.rateLimiterMu.Lock()
	if newCfg.Bot.RateLimiting != nil && newCfg.Bot.RateLimiting.Enabled {
		w := time.Duration(newCfg.Bot.RateLimiting.Window) * time.Second
		b.rateLimiter = NewRateLimiter(w)
	} else {
		b.rateLimiter = nil
	}
	b.rateLimiterMu.Unlock()

	oldNames := config.IRCNetworkNames(enabledNetworks(oldCfg.IRC.Networks))
	newNames := config.IRCNetworkNames(enabledNetworks(newCfg.IRC.Networks))

	for _, name := range channelListDifference(oldNames, newNames) { // present in old, not new: removed
		b.disconnectNetwork(name)
	}
	for _, name := range channelListDifference(newNames, oldNames) { // present in new, not old: added
		netCfg, ok := config.FindIRCNetworkByName(newCfg.IRC.Networks, name)
		if !ok {
			continue
		}
		guard.Go("irc:"+name, func() { b.runNetwork(netCfg) })
	}
	for _, name := range stringsIntersect(oldNames, newNames) { // present in both
		oldN, ok1 := config.FindIRCNetworkByName(oldCfg.IRC.Networks, name)
		newN, ok2 := config.FindIRCNetworkByName(newCfg.IRC.Networks, name)
		if !ok1 || !ok2 {
			continue
		}
		if oldN.Server != newN.Server || oldN.Port != newN.Port || oldN.Nickname != newN.Nickname ||
			oldN.UseSSL != newN.UseSSL || oldN.TLSSkipVerify != newN.TLSSkipVerify || oldN.Services != newN.Services {
			b.disconnectNetwork(name)
			guard.Go("irc:"+name, func() { b.runNetwork(newN) })
			continue // reconnect already re-joins newN.Channels on connect; skip the hot diff below
		}
		net := b.network(name)
		if net == nil || !net.isConnected() {
			continue
		}
		oldAuto := config.IRChannelNamesAutoJoin(oldN.Channels)
		newAuto := config.IRChannelNamesAutoJoin(newN.Channels)
		for _, ch := range channelListDifference(oldAuto, newAuto) {
			net.conn.Part(ch)
		}
		for _, chName := range channelListDifference(newAuto, oldAuto) {
			if entry, ok := config.FindIRChannelByName(newN.Channels, chName); ok {
				if err := ircJoinWithKey(net.conn, entry); err != nil {
					log.Printf("irc[%s]: rehash join %s: %v", name, chName, err)
				}
			}
		}
	}
	log.Printf("Bot configuration reloaded (networks synced).")
}

// disconnectNetwork quits and removes one network's connection; a no-op if not live.
func (b *Bot) disconnectNetwork(name string) {
	b.networksMu.Lock()
	net := b.networks[name]
	if net != nil {
		delete(b.networks, name)
	}
	b.networksMu.Unlock()

	// Also check pending: the network may still be backing off its initial connect and
	// never have reached b.networks. Without this, removing/disabling an unreachable
	// network is a no-op — its retry loop keeps running and re-registers itself once the
	// server comes back despite no longer being in config.
	b.pendingMu.Lock()
	pend := b.pending[name]
	if pend != nil {
		delete(b.pending, name)
	}
	b.pendingMu.Unlock()

	if net != nil {
		net.conn.QuitMessage = net.FormatQuitMessage("")
		net.conn.Quit() // the goroutine blocked in runNetwork's conn.Loop() returns on its own
	}
	if pend != nil && pend != net {
		pend.requestQuit() // cancels connectWithRetry; runNetwork logs and returns
	}
}

// enabledNetworks filters out networks with enabled: false, for the add/remove diff in
// ApplyLiveConfig (disabling a network is treated the same as removing it; re-enabling,
// the same as adding it back).
func enabledNetworks(nets []config.IRCNetworkConfig) []config.IRCNetworkConfig {
	out := make([]config.IRCNetworkConfig, 0, len(nets))
	for _, n := range nets {
		if n.IsEnabled() {
			out = append(out, n)
		}
	}
	return out
}

func stringsIntersect(a, b []string) []string {
	setB := make(map[string]struct{}, len(b))
	for _, x := range b {
		setB[x] = struct{}{}
	}
	var out []string
	for _, x := range a {
		if _, ok := setB[x]; ok {
			out = append(out, x)
		}
	}
	return out
}

// RequestQuit sends QUIT to every connected network with FormatQuitMessage(override).
func (b *Bot) RequestQuit(override string) {
	for _, net := range b.networksSnapshot() {
		if !net.isConnected() {
			continue
		}
		net.conn.QuitMessage = net.FormatQuitMessage(override)
		net.conn.Quit()
	}
}

// IsConnected returns true if the bot is connected to at least one IRC network.
func (b *Bot) IsConnected() bool {
	for _, net := range b.networksSnapshot() {
		if net.isConnected() {
			return true
		}
	}
	return false
}

// IsAuthenticated returns true if the bot is authenticated with services (SASL) on at
// least one network.
func (b *Bot) IsAuthenticated() bool {
	for _, net := range b.networksSnapshot() {
		if net.IsAuthenticated() {
			return true
		}
	}
	return false
}

// Broadcast sends a message to multiple channels. Each entry may be prefixed
// "networkName:#chan"; a bare "#chan" falls back to the first configured network
// (back-compat with single-network configs). Long text is split to fit IRC line limits.
func (b *Bot) Broadcast(channels []string, message string) {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return
	}
	defaultNet := ""
	if nets := b.getCfg().IRC.Networks; len(nets) > 0 {
		defaultNet = nets[0].Name
	}
	for _, raw := range channels {
		netName, chName := config.SplitNetworkChannel(raw, defaultNet)
		net := b.network(netName)
		if net == nil {
			log.Printf("broadcast: unknown network %q for channel %q", netName, chName)
			continue
		}
		net.broadcastOne(chName, msg)
	}
}

// broadcastOne sends message to one channel on this network, split to fit IRC line limits.
func (b *ircNetwork) broadcastOne(channel, msg string) {
	for _, chunk := range splitUTF8ByByteBudget(msg, ircTextBudget) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		b.sendPrivmsg(channel, b.sanitize(chunk))
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
}

// SendMessage sends a message to a channel or user on the given network (used by the
// web server). An empty network falls back to the first configured network.
func (b *Bot) SendMessage(network, target, message string) {
	net := b.networkOrDefault(network)
	if net == nil {
		log.Printf("SendMessage: no live network %q (target %q)", network, target)
		return
	}
	net.sendPrivmsg(target, message)
}

// JoinChannelSession joins a channel on the given network for this process only (not
// persisted to config). Rejoined on IRC reconnect in-process.
func (b *Bot) JoinChannelSession(network string, entry config.IRChannel) error {
	net := b.networkOrDefault(network)
	if net == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	return net.joinChannelSession(entry)
}

// PartChannelSession parts a session-only join on the given network and forgets it.
func (b *Bot) PartChannelSession(network, name string) error {
	net := b.networkOrDefault(network)
	if net == nil {
		return fmt.Errorf("unknown network %q", network)
	}
	return net.partChannelSession(name)
}

// ListSessionChannels returns session-only join entries for the given network (web admin).
func (b *Bot) ListSessionChannels(network string) []config.IRChannel {
	net := b.networkOrDefault(network)
	if net == nil {
		return nil
	}
	return net.listSessionChannels()
}

// NotifyAdmins sends a private message to every logged-in administrator, on whichever
// network they logged in on.
func (b *Bot) NotifyAdmins(message string) {
	b.loginsMu.RLock()
	keys := make([]string, 0, len(b.loggedInAdmins))
	for k := range b.loggedInAdmins {
		keys = append(keys, k)
	}
	b.loginsMu.RUnlock()
	for _, key := range keys {
		netName, nick, ok := splitAdminSessionKey(key)
		if !ok {
			continue
		}
		if net := b.network(netName); net != nil {
			net.sendPrivmsg(nick, message)
		}
	}
}

// NotifyLoggedInAdminsNotice sends a NOTICE to every admin in an active !admin session,
// on whichever network they logged in on.
func (b *Bot) NotifyLoggedInAdminsNotice(message string) {
	b.loginsMu.RLock()
	keys := make([]string, 0, len(b.loggedInAdmins))
	for k := range b.loggedInAdmins {
		keys = append(keys, k)
	}
	b.loginsMu.RUnlock()
	msg := b.sanitize(message)
	for _, key := range keys {
		netName, nick, ok := splitAdminSessionKey(key)
		if !ok {
			continue
		}
		net := b.network(netName)
		if net == nil || !net.isConnected() {
			continue
		}
		net.sendNotice(nick, msg)
	}
}

// NotifyLoggedInAdminsRehashSummary sends a header NOTICE plus one NOTICE per diff line,
// splitting long lines to stay under ircTextBudget.
func (b *Bot) NotifyLoggedInAdminsRehashSummary(source, timeRFC3339 string, diffLines []string) {
	if !b.IsConnected() {
		return
	}
	b.NotifyLoggedInAdminsNotice(fmt.Sprintf("Config rehash (%s) at %s", b.sanitize(source), timeRFC3339))
	for _, d := range diffLines {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		for _, chunk := range splitUTF8ByByteBudget(d, ircTextBudget) {
			if strings.TrimSpace(chunk) == "" {
				continue
			}
			b.NotifyLoggedInAdminsNotice(chunk)
		}
	}
}
