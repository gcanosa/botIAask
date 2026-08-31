package irc

import (
	"crypto/tls"
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

// runNetwork connects one network, registers it in b.networks, blocks in its event
// loop, and unregisters it when the loop exits (Quit() or a fatal disconnect).
func (b *Bot) runNetwork(netCfg config.IRCNetworkConfig) {
	n, err := b.connectNetwork(netCfg)
	if err != nil {
		log.Printf("irc[%s]: connect failed permanently: %v", netCfg.Name, err)
		return
	}
	b.networksMu.Lock()
	b.networks[netCfg.Name] = n
	b.networksMu.Unlock()

	n.conn.Loop() // blocks; returns when Quit()/a fatal error tears the connection down

	b.networksMu.Lock()
	delete(b.networks, netCfg.Name)
	b.networksMu.Unlock()
}

// connectNetwork builds one ircNetwork, registers its event callbacks, and connects
// with capped exponential backoff (same retry policy the bot has always used for its
// single connection). Callback bodies are the same as before multi-network support,
// scoped to this network via the *ircNetwork receiver.
func (b *Bot) connectNetwork(netCfg config.IRCNetworkConfig) (*ircNetwork, error) {
	n := &ircNetwork{Bot: b, name: netCfg.Name, channelMembers: make(map[string]map[string]struct{})}

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

		if strings.HasPrefix(message, "\x01") && strings.HasSuffix(message, "\x01") {
			ctcpContent := message[1 : len(message)-1]
			if strings.HasPrefix(ctcpContent, "ACTION ") {
				actionMsg := ctcpContent[7:]
				logger.LogChannelEvent(n.name, target, logger.EventAction, sender, actionMsg, "")
				if b.tracker != nil {
					b.tracker.LogAction(sender)
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
				b.tracker.LogMessage(sender)
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
			b.tracker.LogJoin()
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
			b.tracker.LogPart()
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
			b.tracker.LogPart()
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
		if b.getCfg().Bot.Debug {
			log.Printf("irc[%s]: disconnected from IRC server", n.name)
		}
	})

	// Initial connect: ircevent only enters its reconnect path after Loop() runs; a failed
	// first Connect() returns here and never reaches Loop(), so this is a daemon — retry
	// forever with capped exponential backoff rather than giving up and leaving idle.
	backoff := 2 * time.Second
	const maxBackoff = 2 * time.Minute
	for attempt := 1; ; attempt++ {
		if err := n.conn.Connect(); err == nil {
			return n, nil
		} else {
			log.Printf("irc[%s]: connect attempt %d failed (retrying in %s): %v", n.name, attempt, backoff, err)
		}
		time.Sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// ApplyLiveConfig swaps in a new config, then reconciles the live network set against
// newCfg.IRC.Networks: added networks are connected, removed ones are torn down,
// endpoint changes (server/port/nick/TLS/SASL) reconnect just that one network, and
// channel-list-only changes are hot join/part'd on the existing connection. No full
// bot restart is required for any network add/remove/edit.
func (b *Bot) ApplyLiveConfig(newCfg *config.Config) {
	oldCfg := b.cfg.Load()
	b.cfg.Store(newCfg)

	b.rateLimiterMu.Lock()
	if newCfg.Bot.RateLimiting != nil && newCfg.Bot.RateLimiting.Enabled {
		w := time.Duration(newCfg.Bot.RateLimiting.Window) * time.Second
		b.rateLimiter = NewRateLimiter(w)
	} else {
		b.rateLimiter = nil
	}
	b.rateLimiterMu.Unlock()

	oldNames := config.IRCNetworkNames(oldCfg.IRC.Networks)
	newNames := config.IRCNetworkNames(newCfg.IRC.Networks)

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
			oldN.UseSSL != newN.UseSSL || oldN.Services != newN.Services {
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
	delete(b.networks, name)
	b.networksMu.Unlock()
	if net == nil {
		return
	}
	net.conn.QuitMessage = net.FormatQuitMessage("")
	net.conn.Quit() // the goroutine blocked in runNetwork's conn.Loop() returns on its own
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
