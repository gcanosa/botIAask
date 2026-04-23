package irc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"botIAask/ai"
	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/crypto"
	"botIAask/internal/sysinfo"
	"botIAask/logger"
	"botIAask/meta"
	"botIAask/rss"
	"botIAask/stats"
	"botIAask/uploads"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"
	"github.com/ergochat/irc-go/ircutils"
)

// Bot represents the IRC bot instance using the ergochat/irc-go library.
type Bot struct {
	cfg            *config.Config
	aiClient       *ai.Client
	conn           *ircevent.Connection
	prefix         string
	cmdName        string
	adminEnabled   bool
	startTime      time.Time
	connectionTime time.Time

	// Channel membership tracking: channel -> set of users
	channelMembers map[string]map[string]struct{}
	membersMu      sync.RWMutex

	// Rate limiting fields
	rateLimiter *RateLimiter

	// Version tracking
	version string

	// Admin and protection
	ignoreList map[string]bool
	ignoreMu   sync.RWMutex

	// Session management for admins
	loggedInAdmins map[string]bool
	loginsMu       sync.RWMutex

	// Stats
	aiRequests int
	statsMu    sync.Mutex
	tracker    *stats.Tracker

	// Connection status
	connected bool

	// Path for persisting config (e.g. announce_to_irc via !news start/stop)
	configPath string

	// RSS Database for !news
	rssDB *rss.Database

	// Bookmarks Database
	bookmarksDB *bookmarks.Database

	// Uploads Database
	uploadsDB *uploads.Database

	// Crypto Database
	cryptoDB *crypto.Database

	// IRC Authentication status
	authenticated bool
	authMu        sync.RWMutex

	rehashHook   func(source string) error
	rehashHookMu sync.Mutex

	// sessionJoins: runtime-only JOINs (not in config; lost on new process, rejoined on IRC reconnect in-process)
	sessionJoins   []config.IRChannel
	sessionJoinsMu sync.Mutex
}

// NewBot initializes a new Bot instance.
func NewBot(cfg *config.Config, aiClient *ai.Client) *Bot {
	bot := &Bot{
		cfg:            cfg,
		aiClient:       aiClient,
		prefix:         cfg.Bot.CommandPrefix,
		cmdName:        cfg.Bot.CommandName,
		startTime:      time.Now(),
		connectionTime: time.Now(),
		channelMembers: make(map[string]map[string]struct{}),
		version:        meta.Version,
		ignoreList:     make(map[string]bool),
		loggedInAdmins: make(map[string]bool),
	}

	// Initialize rate limiter
	if cfg.Bot.RateLimiting != nil && cfg.Bot.RateLimiting.Enabled {
		window := time.Duration(cfg.Bot.RateLimiting.Window) * time.Second
		bot.rateLimiter = NewRateLimiter(window)
	}

	return bot
}

// SetConfigPath sets the YAML path used when persisting config from IRC (e.g. !news start/stop).
func (b *Bot) SetConfigPath(path string) {
	b.configPath = path
}

// SetRSSDatabase sets the RSS database for the bot
func (b *Bot) SetRSSDatabase(db *rss.Database) {
	b.rssDB = db
}

// persistAnnounceToIRC updates rss.announce_to_irc and writes config to disk. The RSS fetcher reads the same cfg pointer, so the next broadcast in an in-flight Fetch() respects the new value.
func (b *Bot) persistAnnounceToIRC(enabled bool) error {
	v := enabled
	b.cfg.RSS.AnnounceToIRC = &v
	path := b.configPath
	if path == "" {
		path = config.DefaultConfigPath
	}
	return config.SaveConfig(path, b.cfg)
}

// SetBookmarksDatabase sets the bookmarks database for the bot
func (b *Bot) SetBookmarksDatabase(db *bookmarks.Database) {
	b.bookmarksDB = db
}

// SetStatsTracker sets the stats tracker for the bot
func (b *Bot) SetStatsTracker(t *stats.Tracker) {
	b.tracker = t
}

// SetUploadsDatabase sets the uploads database for the bot
func (b *Bot) SetUploadsDatabase(db *uploads.Database) {
	b.uploadsDB = db
}

// SetCryptoDatabase sets the crypto database for the bot
func (b *Bot) SetCryptoDatabase(db *crypto.Database) {
	b.cryptoDB = db
}

// SetRehashHook registers the process-wide live reload handler (set from main).
func (b *Bot) SetRehashHook(fn func(source string) error) {
	b.rehashHookMu.Lock()
	b.rehashHook = fn
	b.rehashHookMu.Unlock()
}

// RunRehash invokes the live reload handler (IRC admin, web, or SIGHUP).
func (b *Bot) RunRehash(source string) error {
	b.rehashHookMu.Lock()
	fn := b.rehashHook
	b.rehashHookMu.Unlock()
	if fn == nil {
		return fmt.Errorf("rehash is not configured")
	}
	return fn(source)
}

func configPathOrDefault(b *Bot) string {
	if b.configPath != "" {
		return b.configPath
	}
	return config.DefaultConfigPath
}

func ircChannelTarget(s string) bool {
	return len(s) > 0 && (s[0] == '#' || s[0] == '&')
}

// parseJoinChannelAndKey parses text after "!join ": first word is #channel, remainder (if any) is the channel key.
func parseJoinChannelAndKey(rest string) (name, key string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", ""
	}
	name = fields[0]
	if !ircChannelTarget(name) {
		return "", ""
	}
	if len(fields) > 1 {
		key = strings.Join(fields[1:], " ")
	}
	return name, key
}

func ircJoinWithKey(conn *ircevent.Connection, ch config.IRChannel) error {
	if ch.Password != "" {
		return conn.Send("JOIN", ch.Name, ch.Password)
	}
	return conn.Join(ch.Name)
}

func channelsNotInFold(list []string, ch string) bool {
	for _, x := range list {
		if strings.EqualFold(x, ch) {
			return false
		}
	}
	return true
}

func channelListDifference(a, b []string) []string {
	var out []string
	for _, x := range a {
		if channelsNotInFold(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func (b *Bot) persistIRCChannelsToDisk() error {
	return config.SaveConfig(configPathOrDefault(b), b.cfg)
}

func boolPtrEqualIR(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func (b *Bot) addIRCChannelToConfig(entry config.IRChannel) error {
	for i, existing := range b.cfg.IRC.Channels {
		if strings.EqualFold(existing.Name, entry.Name) {
			if existing.Password == entry.Password && boolPtrEqualIR(existing.AutoJoin, entry.AutoJoin) {
				return nil
			}
			b.cfg.IRC.Channels[i] = entry
			return b.persistIRCChannelsToDisk()
		}
	}
	b.cfg.IRC.Channels = append(b.cfg.IRC.Channels, entry)
	return b.persistIRCChannelsToDisk()
}

func (b *Bot) removeIRCChannelFromConfig(ch string) error {
	out := b.cfg.IRC.Channels[:0]
	for _, existing := range b.cfg.IRC.Channels {
		if !strings.EqualFold(existing.Name, ch) {
			out = append(out, existing)
		}
	}
	b.cfg.IRC.Channels = out
	return b.persistIRCChannelsToDisk()
}

// GetUptime returns the human-readable uptime of the bot.
func (b *Bot) GetUptime() string {
	return formatDuration(time.Since(b.startTime))
}

// FormatQuitMessage builds the IRC QUIT trailing message. Non-empty override (e.g. from !quit text)
// is returned as-is. Otherwise, if irc.quit_message is set, placeholders are expanded; if unset,
// the default is "<app name> <version> Uptime: <uptime>".
func (b *Bot) FormatQuitMessage(override string) string {
	o := strings.TrimSpace(override)
	if o != "" {
		return o
	}
	tmpl := strings.TrimSpace(b.cfg.IRC.QuitMessage)
	if tmpl == "" {
		return fmt.Sprintf("%s %s Uptime: %s", meta.Name, meta.Version, b.GetUptime())
	}
	return b.expandQuitTemplate(tmpl)
}

func (b *Bot) expandQuitTemplate(tmpl string) string {
	r := strings.NewReplacer(
		"{name}", meta.Name,
		"{version}", meta.Version,
		"{uptime}", b.GetUptime(),
		"{nickname}", b.cfg.IRC.Nickname,
	)
	return r.Replace(tmpl)
}

// RequestQuit sends QUIT to IRC with FormatQuitMessage(override) and ends the client loop. No-op if not connected.
func (b *Bot) RequestQuit(override string) {
	b.statsMu.Lock()
	conn := b.conn
	ok := b.connected
	b.statsMu.Unlock()
	if conn == nil || !ok {
		return
	}
	conn.QuitMessage = b.FormatQuitMessage(override)
	conn.Quit()
}

// GetStartTime returns the time the bot was initialized.
func (b *Bot) GetStartTime() time.Time {
	return b.startTime
}

// GetAIRequestCount returns the total number of AI requests processed.
func (b *Bot) GetAIRequestCount() int {
	b.statsMu.Lock()
	defer b.statsMu.Unlock()
	return b.aiRequests
}

// IsConnected returns true if the bot is connected to the IRC server.
func (b *Bot) IsConnected() bool {
	b.statsMu.Lock()
	defer b.statsMu.Unlock()
	return b.connected
}

// IsAuthenticated returns true if the bot is authenticated with services (SASL).
func (b *Bot) IsAuthenticated() bool {
	b.authMu.RLock()
	defer b.authMu.RUnlock()
	return b.authenticated
}

// Broadcast sends a message to multiple channels.
func (b *Bot) Broadcast(channels []string, message string) {
	for _, ch := range channels {
		b.sendPrivmsg(ch, message)
		// Small delay to avoid flooding when broadcasting to many channels
		time.Sleep(500 * time.Millisecond)
	}
}

// IsAdmin checks if a given hostmask or account matches the admin list.
func (b *Bot) IsAdmin(fullHostmask string) bool {
	b.membersMu.RLock()
	defer b.membersMu.RUnlock()
	for _, admin := range b.cfg.Admin.Admins {
		if strings.Contains(fullHostmask, admin) {
			return true
		}
	}
	return false
}

// ApplyLiveConfig swaps in a new config from disk, rebuilds rate limiting, and syncs channel membership without reconnecting.
func (b *Bot) ApplyLiveConfig(newCfg *config.Config) {
	b.membersMu.Lock()
	oldAuto := config.IRChannelNamesAutoJoin(b.cfg.IRC.Channels)
	oldSrv := b.cfg.IRC.Server
	oldPort := b.cfg.IRC.Port
	oldNick := b.cfg.IRC.Nickname
	oldTLS := b.cfg.IRC.UseSSL

	b.cfg = newCfg
	b.prefix = newCfg.Bot.CommandPrefix
	b.cmdName = newCfg.Bot.CommandName
	if newCfg.Bot.RateLimiting != nil && newCfg.Bot.RateLimiting.Enabled {
		w := time.Duration(newCfg.Bot.RateLimiting.Window) * time.Second
		b.rateLimiter = NewRateLimiter(w)
	} else {
		b.rateLimiter = nil
	}
	b.membersMu.Unlock()

	if oldSrv != newCfg.IRC.Server || oldPort != newCfg.IRC.Port || oldNick != newCfg.IRC.Nickname || oldTLS != newCfg.IRC.UseSSL {
		log.Printf("config rehash: irc server/port/nick/tls changed in YAML — reconnect required for those to take effect")
	}

	b.statsMu.Lock()
	conn := b.conn
	ok := b.connected
	b.statsMu.Unlock()

	if conn == nil || !ok {
		log.Printf("Bot configuration reloaded (not connected; channel sync skipped).")
		return
	}

	newAuto := config.IRChannelNamesAutoJoin(newCfg.IRC.Channels)
	for _, ch := range channelListDifference(oldAuto, newAuto) {
		conn.Part(ch)
	}
	for _, chName := range channelListDifference(newAuto, oldAuto) {
		if entry, ok := config.FindIRChannelByName(newCfg.IRC.Channels, chName); ok {
			if err := ircJoinWithKey(conn, entry); err != nil {
				log.Printf("rehash join %s: %v", chName, err)
			}
		}
	}
	log.Printf("Bot configuration reloaded (channels synced).")
}

func (b *Bot) rejoinSessionChannels() {
	b.sessionJoinsMu.Lock()
	chs := append([]config.IRChannel(nil), b.sessionJoins...)
	b.sessionJoinsMu.Unlock()
	if len(chs) == 0 {
		return
	}
	b.statsMu.Lock()
	conn := b.conn
	ok := b.connected
	b.statsMu.Unlock()
	if conn == nil || !ok {
		return
	}
	for _, ch := range chs {
		if err := ircJoinWithKey(conn, ch); err != nil {
			log.Printf("session rejoin %s: %v", ch.Name, err)
		}
	}
}

// JoinChannelSession joins a channel for this process only (not in config). Rejoined on IRC reconnect in-process.
func (b *Bot) JoinChannelSession(entry config.IRChannel) error {
	name := strings.TrimSpace(entry.Name)
	if !ircChannelTarget(name) {
		return fmt.Errorf("invalid channel name")
	}
	if _, ok := config.FindIRChannelByName(b.cfg.IRC.Channels, name); ok {
		return fmt.Errorf("channel already in config; edit autoinjoin or remove from list")
	}
	b.sessionJoinsMu.Lock()
	for _, s := range b.sessionJoins {
		if strings.EqualFold(s.Name, name) {
			b.sessionJoinsMu.Unlock()
			return fmt.Errorf("already in session-join list")
		}
	}
	b.sessionJoinsMu.Unlock()
	b.statsMu.Lock()
	conn := b.conn
	ok := b.connected
	b.statsMu.Unlock()
	if conn == nil || !ok {
		return fmt.Errorf("not connected to IRC")
	}
	if err := ircJoinWithKey(conn, entry); err != nil {
		return err
	}
	b.sessionJoinsMu.Lock()
	b.sessionJoins = append(b.sessionJoins, entry)
	b.sessionJoinsMu.Unlock()
	return nil
}

// PartChannelSession parts a session-only join and forgets it.
func (b *Bot) PartChannelSession(name string) error {
	b.sessionJoinsMu.Lock()
	var out []config.IRChannel
	var partName string
	for _, s := range b.sessionJoins {
		if strings.EqualFold(s.Name, name) {
			partName = s.Name
			continue
		}
		out = append(out, s)
	}
	if partName == "" {
		b.sessionJoinsMu.Unlock()
		return fmt.Errorf("not a session-only join")
	}
	b.sessionJoins = out
	b.sessionJoinsMu.Unlock()
	b.statsMu.Lock()
	conn := b.conn
	ok := b.connected
	b.statsMu.Unlock()
	if conn != nil && ok {
		conn.Part(partName)
	}
	return nil
}

// ListSessionChannels returns session-only join entries (for web admin).
func (b *Bot) ListSessionChannels() []config.IRChannel {
	b.sessionJoinsMu.Lock()
	defer b.sessionJoinsMu.Unlock()
	return append([]config.IRChannel(nil), b.sessionJoins...)
}

// Start connects to the IRC server and starts the bot event loop.
func (b *Bot) Start() error {
	serverAddr := fmt.Sprintf("%s:%d", b.cfg.IRC.Server, b.cfg.IRC.Port)

	// Initialize the connection object
	b.conn = &ircevent.Connection{
		Server:      serverAddr,
		Nick:        b.cfg.IRC.Nickname,
		User:        b.cfg.IRC.Nickname,
		RealName:    b.cfg.IRC.Nickname,
		UseTLS:      b.cfg.IRC.UseSSL,
		Debug:       b.cfg.Bot.Debug,
		RequestCaps: []string{"server-time", "message-tags", "sasl"},
	}

	// SASL Authentication setup
	if b.cfg.IRC.Services.Enabled {
		b.conn.SASLLogin = b.cfg.IRC.Services.Username
		b.conn.SASLPassword = b.cfg.IRC.Services.Password
		if b.cfg.Bot.Debug {
			log.Printf("[DEBUG] SASL Authentication enabled for user: %s", b.conn.SASLLogin)
		}
	}

	// Handle successful authentication
	// 900: RPL_LOGGEDIN, 903: RPL_SASLSUCCESS
	authSuccess := func(e ircmsg.Message) {
		b.authMu.Lock()
		b.authenticated = true
		b.authMu.Unlock()
		if b.cfg.Bot.Debug {
			log.Println("[DEBUG] Successfully authenticated with services.")
		}
	}
	b.conn.AddCallback("900", authSuccess)
	b.conn.AddCallback("903", authSuccess)

	// Handle failed authentication
	// 902: ERR_NICKLOCKED, 904: ERR_SASLFAIL, etc.
	authFail := func(e ircmsg.Message) {
		b.authMu.Lock()
		b.authenticated = false
		b.authMu.Unlock()
		log.Printf("[ERROR] IRC Authentication failed: %s", e.Params[len(e.Params)-1])
	}
	b.conn.AddCallback("902", authFail)
	b.conn.AddCallback("904", authFail)
	b.conn.AddCallback("905", authFail)

	// Handle connection established event
	b.conn.AddConnectCallback(func(e ircmsg.Message) {
		log.Printf("Connected to %s! Joining channels...", serverAddr)
		b.connectionTime = time.Now()
		b.statsMu.Lock()
		b.connected = true
		b.statsMu.Unlock()
		for _, channel := range b.cfg.IRC.Channels {
			if !channel.AutoJoinEnabled() {
				if b.cfg.Bot.Debug {
					log.Printf("[DEBUG] Skipping auto-join (auto_join: false): %s", channel.Name)
				}
				continue
			}
			if b.cfg.Bot.Debug {
				if channel.Password != "" {
					log.Printf("[DEBUG] Joining channel: %s (key set)", channel.Name)
				} else {
					log.Printf("[DEBUG] Joining channel: %s", channel.Name)
				}
			}
			if err := ircJoinWithKey(b.conn, channel); err != nil {
				log.Printf("join %s: %v", channel.Name, err)
			}
		}
		b.rejoinSessionChannels()
	})

	// Handle PRIVMSG (messages in channels or private)
	b.conn.AddCallback("PRIVMSG", func(e ircmsg.Message) {
		target := e.Params[0] // Channel or Nick
		message := e.Params[1]
		sender := e.Nick()

		if b.cfg.Bot.Debug {
			log.Printf("[DEBUG] PRIVMSG received - Sender: %s, Target: %s, Content: %s", sender, target, message)
		}

		if strings.HasPrefix(message, "\x01") && strings.HasSuffix(message, "\x01") {
			ctcpContent := message[1 : len(message)-1]
			if strings.HasPrefix(ctcpContent, "ACTION ") {
				actionMsg := ctcpContent[7:]
				logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventAction, sender, actionMsg, "")
				if b.tracker != nil {
					b.tracker.LogAction(sender)
				}
			} else {
				b.handleCTCPRequest(sender, target, ctcpContent)
			}
		} else {
			logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventMessage, sender, message, "")
			if b.tracker != nil {
				b.tracker.LogMessage(sender)
			}
			b.handleCommand(target, message, sender, e.Source)
		}
	})

	b.conn.AddCallback("NOTICE", func(e ircmsg.Message) {
		if len(e.Params) < 2 {
			return
		}
		target := e.Params[0]
		message := e.Params[1]
		sender := e.Nick()
		logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventNotice, sender, message, "")
	})

	b.conn.AddCallback("JOIN", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		target := e.Params[0] // Channel
		sender := e.Nick()
		logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventJoin, sender, "", "")

		b.membersMu.Lock()
		if _, exists := b.channelMembers[target]; !exists {
			b.channelMembers[target] = make(map[string]struct{})
		}
		b.channelMembers[target][sender] = struct{}{}
		b.membersMu.Unlock()

		if b.tracker != nil {
			b.tracker.LogJoin()
			b.updateTrackerAdmins()
		}

		if b.bookmarksDB != nil && bookmarks.IRCCaseFoldNick(sender) != bookmarks.IRCCaseFoldNick(b.cfg.IRC.Nickname) {
			rems, err := b.bookmarksDB.ListReminders(sender)
			if err != nil {
				if b.cfg.Bot.Debug {
					log.Printf("[DEBUG] ListReminders on JOIN: %v", err)
				}
			} else {
				const maxJoinNoteBytes = 380
				for _, r := range rems {
					note := truncateReminderNotice(r.Note, maxJoinNoteBytes)
					b.sendNotice(sender, fmt.Sprintf("[Reminder %s] %s", r.PublicID, note))
				}
			}
		}
	})

	b.conn.AddCallback("PART", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		target := e.Params[0] // Channel
		sender := e.Nick()
		message := ""
		if len(e.Params) > 1 {
			message = e.Params[1]
		}
		logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventPart, sender, message, "")

		b.membersMu.Lock()
		if members, exists := b.channelMembers[target]; exists {
			delete(members, sender)
		}
		b.membersMu.Unlock()

		if b.tracker != nil {
			b.tracker.LogPart()
			b.updateTrackerAdmins()
		}
	})

	b.conn.AddCallback("KICK", func(e ircmsg.Message) {
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
		logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventKick, sender, message, kicked)

		b.membersMu.Lock()
		if members, exists := b.channelMembers[target]; exists {
			delete(members, kicked)
		}
		b.membersMu.Unlock()

		if b.tracker != nil {
			b.updateTrackerAdmins()
		}
	})

	// QUIT and NICK are not channel-specific, we'll log them globally or skip.
	b.conn.AddCallback("QUIT", func(e ircmsg.Message) {
		sender := e.Nick()
		message := ""
		if len(e.Params) > 0 {
			message = e.Params[0]
		}
		// For quits, we log to all configured channels as we might not have a full state tracker
		for _, channel := range b.cfg.IRC.Channels {
			logger.LogChannelEvent(b.cfg.IRC.Server, channel.Name, logger.EventQuit, sender, message, "")
		}

		b.membersMu.Lock()
		for _, members := range b.channelMembers {
			delete(members, sender)
		}
		b.membersMu.Unlock()

		if b.tracker != nil {
			b.tracker.LogPart()
			b.updateTrackerAdmins()
		}
	})

	b.conn.AddCallback("NICK", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		sender := e.Nick()
		newNick := e.Params[0]
		for _, channel := range b.cfg.IRC.Channels {
			logger.LogChannelEvent(b.cfg.IRC.Server, channel.Name, logger.EventNick, sender, newNick, "")
		}

		b.membersMu.Lock()
		for _, members := range b.channelMembers {
			if _, exists := members[sender]; exists {
				delete(members, sender)
				members[newNick] = struct{}{}
			}
		}
		b.membersMu.Unlock()

		b.loginsMu.Lock()
		if b.loggedInAdmins[sender] {
			delete(b.loggedInAdmins, sender)
			b.loggedInAdmins[newNick] = true
		}
		b.loginsMu.Unlock()

		if b.tracker != nil {
			b.updateTrackerAdmins()
		}
	})

	// Handle disconnection events
	b.conn.AddDisconnectCallback(func(e ircmsg.Message) {
		b.statsMu.Lock()
		b.connected = false
		b.statsMu.Unlock()
		if b.cfg.Bot.Debug {
			log.Println("Disconnected from IRC server")
		}
	})

	// Initial connect: ircevent only enters its reconnect path after Loop() runs; a failed first
	// Connect() returns here and never reaches Loop(), so transient TLS/DNS/handshake failures need retries.
	const maxIRCConnectAttempts = 5
	connectBackoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	var err error
	for attempt := 1; attempt <= maxIRCConnectAttempts; attempt++ {
		err = b.conn.Connect()
		if err == nil {
			break
		}
		log.Printf("IRC connect attempt %d/%d failed: %v", attempt, maxIRCConnectAttempts, err)
		if attempt < maxIRCConnectAttempts {
			time.Sleep(connectBackoff[attempt-1])
		}
	}
	if err != nil {
		return fmt.Errorf("failed to connect to IRC server after %d attempts: %w", maxIRCConnectAttempts, err)
	}

	// The Loop handles reconnection after disconnect
	b.conn.Loop()

	return nil
}

// handleCommand checks for the commands and interacts with the AI client or management functions.
func (b *Bot) handleCommand(target, message, sender, source string) {
	isAdmin := b.IsAdmin(source)

	b.loginsMu.RLock()
	isLoggedInAdmin := b.loggedInAdmins[sender]
	b.loginsMu.RUnlock()

	// !help command
	if strings.HasPrefix(message, b.prefix+"help") {
		public := fmt.Sprintf("Commands: %s%s <query>, %sbc <expr>, %snews [limit], %sbookmark ADD <URL> [nickname] | %sbookmark FIND <text>, %suptime, %sspec, %spaste, %supload, %sdownload [N], %seuro, %speso, %scrypto, %sreminder add/del/list/read",
			b.prefix, b.cmdName, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix)
		if isAdmin && isLoggedInAdmin {
			admin := fmt.Sprintf("Admin: %sadmin off, %sjoin #chan [key], %spart #chan, %signore nick, %sstats, %ssay #chan msg, %squit msg, %srehash, %snews on/off, %snews start/stop (IRC announce), %sop [nick], %sdeop [nick], %svoice [nick], %sdevoice [nick], %sticket pending/approve/cancel [ID]",
				b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix)
			b.sendPrivmsgMentionedLines(target, sender, public, admin)
		} else if isAdmin {
			merged := public + fmt.Sprintf(" | Admin: Auth required using %sadmin", b.prefix)
			b.sendPrivmsgMentionedLines(target, sender, merged)
		} else {
			b.sendPrivmsgMentionedLines(target, sender, public)
		}
		return
	}

	// Session commands
	if strings.HasPrefix(message, b.prefix+"admin") {
		parts := strings.Fields(message)
		if len(parts) > 1 && parts[1] == "off" {
			b.loginsMu.Lock()
			delete(b.loggedInAdmins, sender)
			b.loginsMu.Unlock()
			if b.tracker != nil {
				b.updateTrackerAdmins()
			}
			b.sendPrivmsg(target, fmt.Sprintf("%s logged out of admin session.", sender))
		} else {
			if isAdmin {
				if isLoggedInAdmin {
					b.sendNotice(sender, "You are already logged in to an admin session.")
					return
				}
				b.loginsMu.Lock()
				b.loggedInAdmins[sender] = true
				recipients := make([]string, 0, len(b.loggedInAdmins))
				for n := range b.loggedInAdmins {
					recipients = append(recipients, n)
				}
				b.loginsMu.Unlock()
				if b.tracker != nil {
					b.updateTrackerAdmins()
				}
				b.sendPrivmsg(target, fmt.Sprintf("%s logged in to admin session.", sender))
				b.notifyLoggedInAdminsPendingApprovals(recipients)
			} else {
				if b.tracker != nil {
					b.tracker.LogFailedAuth()
				}
				b.sendPrivmsg(target, fmt.Sprintf("%s not authorized.", sender))
			}
		}
		return
	}

	// Admin commands
	if isAdmin && isLoggedInAdmin {
		if strings.HasPrefix(message, b.prefix+"join ") {
			rest := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+"join "))
			chName, chKey := parseJoinChannelAndKey(rest)
			if chName == "" {
				return
			}
			entry := config.IRChannel{Name: chName, Password: chKey}
			if err := ircJoinWithKey(b.conn, entry); err != nil {
				log.Printf("!join: %v", err)
			}
			if err := b.addIRCChannelToConfig(entry); err != nil {
				log.Printf("persist irc channels: %v", err)
				b.sendPrivmsg(target, fmt.Sprintf("Joined %s but failed to save config: %v", chName, err))
			}
			if b.tracker != nil {
				b.tracker.LogAdminCommand()
			}
			if chKey != "" {
				b.sendPrivmsg(target, fmt.Sprintf("Joining %s (channel key stored in config)...", chName))
			} else {
				b.sendPrivmsg(target, fmt.Sprintf("Joining %s...", chName))
			}
			return
		}
		if strings.HasPrefix(message, b.prefix+"part") {
			parts := strings.Fields(message)
			channel := target
			if len(parts) > 1 {
				channel = parts[1]
			}
			b.conn.Part(channel)
			if ircChannelTarget(channel) {
				if err := b.removeIRCChannelFromConfig(channel); err != nil {
					log.Printf("persist irc channels: %v", err)
					b.sendPrivmsg(target, fmt.Sprintf("Parted %s but failed to save config: %v", channel, err))
				}
			}
			if b.tracker != nil {
				b.tracker.LogAdminCommand()
			}
			return
		}
		if message == b.prefix+"rehash" {
			if err := b.RunRehash(sender); err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("Rehash failed: %v", err))
			} else {
				b.sendPrivmsg(target, "Config reloaded from disk.")
			}
			if b.tracker != nil {
				b.tracker.LogAdminCommand()
			}
			return
		}
		if strings.HasPrefix(message, b.prefix+"ignore ") {
			user := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+"ignore "))
			if user != "" {
				b.ignoreMu.Lock()
				b.ignoreList[strings.ToLower(user)] = true
				b.ignoreMu.Unlock()
				if b.tracker != nil {
					b.tracker.LogAdminCommand()
				}
				b.sendPrivmsg(target, fmt.Sprintf("Now ignoring %s", user))
			}
			return
		}
		if strings.HasPrefix(message, b.prefix+"stats") {
			if b.tracker != nil {
				b.tracker.LogAdminCommand()
			}
			b.sendAdminStats(target, sender)
			return
		}
		if strings.HasPrefix(message, b.prefix+"say ") {
			parts := strings.SplitN(message, " ", 3)
			if len(parts) >= 3 {
				ch := parts[1]
				msg := parts[2]
				b.sendPrivmsg(ch, msg)
				if b.tracker != nil {
					b.tracker.LogAdminCommand()
				}
			}
			return
		}
		if strings.HasPrefix(message, b.prefix+"quit") {
			reason := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+"quit"))
			if b.tracker != nil {
				b.tracker.LogAdminCommand()
			}
			b.RequestQuit(reason)
			return
		}

		// Operator and Voice management
		if strings.HasPrefix(target, "#") {
			parts := strings.Fields(message)
			if len(parts) > 0 {
				cmd := strings.ToLower(parts[0])
				targetNick := sender
				if len(parts) > 1 {
					targetNick = parts[1]
				}

				switch cmd {
				case b.prefix + "op":
					b.conn.Send("MODE", target, "+o", targetNick)
					if b.tracker != nil {
						b.tracker.LogAdminCommand()
					}
					return
				case b.prefix + "deop":
					b.conn.Send("MODE", target, "-o", targetNick)
					if b.tracker != nil {
						b.tracker.LogAdminCommand()
					}
					return
				case b.prefix + "voice":
					b.conn.Send("MODE", target, "+v", targetNick)
					if b.tracker != nil {
						b.tracker.LogAdminCommand()
					}
					return
				case b.prefix + "devoice":
					b.conn.Send("MODE", target, "-v", targetNick)
					if b.tracker != nil {
						b.tracker.LogAdminCommand()
					}
					return
				}
			}
		}
	} else if isAdmin {
		// Log failed attempts to use admin commands without session
		if strings.HasPrefix(message, b.prefix) {
			adminCmds := []string{"join", "part", "ignore", "stats", "say", "quit", "rehash", "op", "deop", "voice", "devoice"}
			parts := strings.Fields(message)
			if len(parts) > 0 {
				cmd := strings.TrimPrefix(parts[0], b.prefix)
				for _, ac := range adminCmds {
					if cmd == ac {
						if b.tracker != nil {
							b.tracker.LogFailedAuth()
						}
						break
					}
				}
			}
		}
	}

	// Check if user is ignored
	b.ignoreMu.RLock()
	ignored := b.ignoreList[strings.ToLower(sender)]
	b.ignoreMu.RUnlock()
	if ignored {
		return
	}

	// Handle !uptime command
	if strings.HasPrefix(message, b.prefix+"uptime") {
		appUptime := time.Since(b.startTime)
		sessionUptime := time.Since(b.connectionTime)

		appUptimeStr := formatDuration(appUptime)
		sessionUptimeStr := formatDuration(sessionUptime)

		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("Bot uptime: App=%s, Session=%s", appUptimeStr, sessionUptimeStr)))
		return
	}

	// Handle !bc command (Calculator)
	if strings.HasPrefix(message, b.prefix+"bc ") {
		exprStr := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+"bc "))
		if exprStr == "" {
			b.sendPrivmsg(target, fmt.Sprintf("Usage: %sbc <expression>, e.g., %sbc 5+5", b.prefix, b.prefix))
			return
		}

		result, err := EvaluateExpression(exprStr)
		if err != nil {
			b.sendPrivmsg(target, fmt.Sprintf("@%s: Error: %v", sender, err))
			return
		}

		b.sendPrivmsg(target, fmt.Sprintf("@%s: %s = %s", sender, exprStr, result))
		return
	}

	// Handle !euro command
	if strings.HasPrefix(message, b.prefix+"euro") {
		b.handleEuroCommand(target)
		return
	}

	// Handle !peso command
	if strings.HasPrefix(message, b.prefix+"peso") {
		b.handlePesoCommand(target)
		return
	}

	// Handle !crypto command
	if strings.HasPrefix(message, b.prefix+"crypto") {
		b.handleCryptoCommand(target)
		return
	}

	// Handle !news command
	if strings.HasPrefix(message, b.prefix+"news") {
		parts := strings.Fields(message)

		// Persist rss.announce_to_irc (global IRC broadcast on/off)
		if len(parts) > 1 && (parts[1] == "start" || parts[1] == "stop") {
			if isAdmin && isLoggedInAdmin {
				enabled := parts[1] == "start"
				if err := b.persistAnnounceToIRC(enabled); err != nil {
					b.sendPrivmsg(target, fmt.Sprintf("Failed to save config: %v", err))
				} else if enabled {
					b.sendPrivmsg(target, "RSS posting to IRC: ON (saved to config; applies to upcoming fetches).")
				} else {
					b.sendPrivmsg(target, "RSS posting to IRC: OFF (saved to config; in-flight fetch stops announcing).")
				}
			} else {
				b.sendPrivmsg(target, "Not authorized or session expired.")
			}
			return
		}

		// In-memory news toggling
		if len(parts) > 1 && (parts[1] == "on" || parts[1] == "off") {
			if isAdmin && isLoggedInAdmin {
				if parts[1] == "on" {
					found := false
					b.membersMu.Lock()
					for _, ch := range b.cfg.RSS.Channels {
						if strings.EqualFold(ch, target) {
							found = true
							break
						}
					}
					if !found {
						b.cfg.RSS.Channels = append(b.cfg.RSS.Channels, target)
					}
					b.membersMu.Unlock()
					b.sendPrivmsg(target, fmt.Sprintf("News enabled for %s (current session only).", target))
				} else {
					b.membersMu.Lock()
					for i, ch := range b.cfg.RSS.Channels {
						if strings.EqualFold(ch, target) {
							b.cfg.RSS.Channels = append(b.cfg.RSS.Channels[:i], b.cfg.RSS.Channels[i+1:]...)
							break
						}
					}
					b.membersMu.Unlock()
					b.sendPrivmsg(target, fmt.Sprintf("News disabled for %s (current session only).", target))
				}
			} else {
				b.sendPrivmsg(target, "Not authorized or session expired.")
			}
			return
		}

		// Check if news enabled for this channel
		isNewsChannel := false
		b.membersMu.RLock()
		for _, ch := range b.cfg.RSS.Channels {
			if strings.EqualFold(ch, target) {
				isNewsChannel = true
				break
			}
		}
		b.membersMu.RUnlock()

		if !isNewsChannel && !(isAdmin && isLoggedInAdmin) {
			return
		}

		if b.rssDB == nil {
			b.sendPrivmsg(target, "RSS database not initialized.")
			return
		}

		limit := 5
		if len(parts) > 1 {
			if l, err := fmt.Sscanf(parts[1], "%d", &limit); err == nil && l > 0 {
				if limit > 10 {
					limit = 10
				}
			}
		}

		entries, err := b.rssDB.GetLastNews(limit)
		if err != nil {
			b.sendPrivmsg(target, fmt.Sprintf("Error fetching news: %v", err))
			return
		}

		if len(entries) == 0 {
			b.sendPrivmsg(target, "No news items found.")
			return
		}

		for _, e := range entries {
			displayLink := e.ShortLink
			if displayLink == "" && e.Link != "" {
				displayLink = rss.ShortenURL(e.Link)
			}

			msg := rss.FormatIRCNewsLine(e, displayLink)
			b.sendPrivmsg(target, msg)
			time.Sleep(1 * time.Second) // Throttling
		}
		return
	}

	// Handle !bookmark command
	if strings.HasPrefix(message, b.prefix+"bookmark") {
		if b.bookmarksDB == nil {
			b.sendPrivmsg(target, "Bookmarks database not initialized.")
			return
		}

		body := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+"bookmark"))
		if body == "" {
			b.sendPrivmsg(target, fmt.Sprintf("Usage: %sbookmark ADD <URL> [nickname] | %sbookmark FIND <text>", b.prefix, b.prefix))
			return
		}
		firstSpace := strings.IndexByte(body, ' ')
		var sub, rest string
		if firstSpace < 0 {
			sub = strings.ToUpper(body)
			rest = ""
		} else {
			sub = strings.ToUpper(strings.TrimSpace(body[:firstSpace]))
			rest = strings.TrimSpace(body[firstSpace:])
		}

		switch sub {
		case "ADD":
			parts := strings.Fields(rest)
			if len(parts) < 1 {
				b.sendPrivmsg(target, fmt.Sprintf("Usage: %sbookmark ADD <URL> [nickname]", b.prefix))
				return
			}
			urlStr := parts[0]
			nickname := sender
			if len(parts) >= 2 {
				nickname = parts[1]
			}
			if !bookmarks.ValidBookmarkURL(urlStr) {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: Invalid URL. Use http:// or https:// with a host (encode spaces in the URL).", sender))
				return
			}
			if !isAdmin {
				tenMinutesAgo := time.Now().Add(-10 * time.Minute)
				count, err := b.bookmarksDB.CountUserBookmarksSince(sender, tenMinutesAgo)
				if err != nil {
					log.Printf("Error checking bookmark rate limit: %v", err)
				} else if count >= 3 {
					b.sendPrivmsg(target, fmt.Sprintf("@%s: Rate limit reached. You can only add 3 bookmarks every 10 minutes.", sender))
					return
				}
			}
			hostname := "unknown"
			if idx := strings.Index(source, "@"); idx != -1 {
				hostname = source[idx+1:]
			}
			id, err := b.bookmarksDB.AddBookmark(urlStr, nickname, hostname)
			if err != nil {
				if strings.Contains(err.Error(), "UNIQUE constraint failed") {
					b.sendPrivmsg(target, fmt.Sprintf("@%s: URL already bookmarked.", sender))
				} else {
					b.sendPrivmsg(target, fmt.Sprintf("@%s: Error adding bookmark: %v", sender, err))
				}
				return
			}
			prefix := "\x0303,01[BOOKMARK]\x03"
			b.sendPrivmsg(target, fmt.Sprintf("%s @%s: Bookmark added successfully! (ID: %d)", prefix, sender, id))

		case "FIND":
			if strings.TrimSpace(rest) == "" {
				b.sendPrivmsg(target, fmt.Sprintf("Usage: %sbookmark FIND <text>", b.prefix))
				return
			}
			list, err := b.bookmarksDB.FindBookmarksByURLContains(strings.TrimSpace(rest), 10)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: Error searching bookmarks: %v", sender, err))
				return
			}
			if len(list) == 0 {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: No bookmarks matched that URL pattern.", sender))
				return
			}
			pairs := make([]string, 0, len(list))
			for _, bm := range list {
				pairs = append(pairs, fmt.Sprintf("#%d %s", bm.ID, bm.URL))
			}
			out := strings.Join(pairs, " | ")
			b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s", sender, out)))

		default:
			b.sendPrivmsg(target, fmt.Sprintf("Usage: %sbookmark ADD <URL> [nickname] | %sbookmark FIND <text>", b.prefix, b.prefix))
		}
		return
	}

	if strings.HasPrefix(message, b.prefix+"reminder") {
		if b.bookmarksDB == nil {
			b.sendPrivmsg(target, "Bookmarks database not initialized.")
			return
		}
		body := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+"reminder"))
		if body == "" {
			b.sendPrivmsg(target, fmt.Sprintf("Usage: %sreminder add <note> | %sreminder del <id> | %sreminder list | %sreminder read <id>", b.prefix, b.prefix, b.prefix, b.prefix))
			return
		}
		firstSpace := strings.IndexByte(body, ' ')
		var sub, rest string
		if firstSpace < 0 {
			sub = body
			rest = ""
		} else {
			sub = strings.TrimSpace(body[:firstSpace])
			rest = strings.TrimSpace(body[firstSpace:])
		}
		switch strings.ToLower(sub) {
		case "add":
			if rest == "" {
				b.sendPrivmsg(target, fmt.Sprintf("Usage: %sreminder add <note>", b.prefix))
				return
			}
			id, err := b.bookmarksDB.AddReminder(sender, rest)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: Error adding reminder: %v", sender, err))
				return
			}
			b.sendPrivmsg(target, fmt.Sprintf("@%s: Reminder added (id %s).", sender, id))
		case "del":
			fields := strings.Fields(rest)
			if len(fields) < 1 {
				b.sendPrivmsg(target, fmt.Sprintf("Usage: %sreminder del <id>", b.prefix))
				return
			}
			pid := fields[0]
			ok, err := b.bookmarksDB.DeleteReminder(sender, pid)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: Error deleting reminder: %v", sender, err))
				return
			}
			if !ok {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: No reminder with that id, or not yours.", sender))
				return
			}
			b.sendPrivmsg(target, fmt.Sprintf("@%s: Reminder %s deleted.", sender, pid))
		case "list":
			if rest != "" {
				b.sendPrivmsg(target, fmt.Sprintf("Usage: %sreminder list", b.prefix))
				return
			}
			rems, err := b.bookmarksDB.ListReminders(sender)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: Error listing reminders: %v", sender, err))
				return
			}
			if len(rems) == 0 {
				return
			}
			const maxListNoteBytes = 120
			for _, r := range rems {
				note := truncateReminderNotice(r.Note, maxListNoteBytes)
				b.sendPrivmsg(target, fmt.Sprintf("@%s: [%s] %s", sender, r.PublicID, note))
			}
		case "read":
			fields := strings.Fields(rest)
			if len(fields) < 1 {
				b.sendPrivmsg(target, fmt.Sprintf("Usage: %sreminder read <id>", b.prefix))
				return
			}
			pid := fields[0]
			r, ok, err := b.bookmarksDB.GetReminder(sender, pid)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: Error reading reminder: %v", sender, err))
				return
			}
			if !ok {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: No reminder with that id, or not yours.", sender))
				return
			}
			b.sendPrivmsg(target, fmt.Sprintf("@%s: [%s] %s", sender, r.PublicID, r.Note))
		default:
			b.sendPrivmsg(target, fmt.Sprintf("Usage: %sreminder add <note> | %sreminder del <id> | %sreminder list | %sreminder read <id>", b.prefix, b.prefix, b.prefix, b.prefix))
		}
		return
	}

	// Handle !spec command (Restored)
	if strings.HasPrefix(message, b.prefix+"spec") {
		spec := "System Prompt: You are a helpful IRC bot. Keep responses concise and suitable for IRC."
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s", sender, spec)))
		return
	}

	// Ticket commands (Admin only)
	if strings.HasPrefix(message, b.prefix+"ticket") {
		if !isAdmin || !isLoggedInAdmin {
			b.sendPrivmsg(target, fmt.Sprintf("@%s: Authorized admins only.", sender))
			return
		}
		if b.uploadsDB == nil {
			b.sendPrivmsg(target, "Uploads system not initialized.")
			return
		}
		parts := strings.Fields(message)
		if len(parts) < 2 {
			b.sendPrivmsg(target, fmt.Sprintf("Usage: %sticket pending/approve/cancel [ID]", b.prefix))
			return
		}
		cmd := parts[1]

		if cmd == "pending" {
			pending, err := b.uploadsDB.GetPendingTickets()
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("Error fetching pending tickets: %v", err))
				return
			}
			if len(pending) == 0 {
				b.sendPrivmsg(target, "No pending tickets.")
				return
			}
			b.sendPrivmsg(target, fmt.Sprintf("Found %d pending ticket(s):", len(pending)))
			for _, t := range pending {
				expiryStr := "Never"
				if t.ExpiresInDays > 0 {
					expiryStr = fmt.Sprintf("%d days", t.ExpiresInDays)
				}
				elapsed := time.Since(t.CreatedAt).Round(time.Minute)
				kind := "paste"
				if t.IsFile() {
					kind = "file"
				}
				b.sendPrivmsg(target, fmt.Sprintf("- [%s] [%s] %s by %s (Requested: %s expiry, Submitted: %s ago)",
					t.TicketID, kind, t.Title, t.Username, expiryStr, elapsed))
				time.Sleep(500 * time.Millisecond) // Slight delay for sanity
			}
		} else if cmd == "approve" {
			if len(parts) < 3 {
				b.sendPrivmsg(target, "Usage: !ticket approve <ID>")
				return
			}
			ticketID := parts[2]
			err := b.uploadsDB.ApproveTicket(ticketID)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("Error approving ticket: %v", err))
				return
			}
			u, _ := b.uploadsDB.GetUploadByTicketID(ticketID)
			pubURL := fmt.Sprintf("%s/p/%s", b.cfg.Web.BaseURL, ticketID)
			if u != nil && u.IsFile() {
				pubURL = fmt.Sprintf("%s/f/%s", b.cfg.Web.BaseURL, ticketID)
			}
			b.sendPrivmsg(target, fmt.Sprintf("Ticket %s approved. View at: %s", ticketID, pubURL))
		} else if cmd == "cancel" {
			if len(parts) < 3 {
				b.sendPrivmsg(target, "Usage: !ticket cancel <ID>")
				return
			}
			ticketID := parts[2]
			err := b.uploadsDB.CancelTicket(ticketID)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("Error cancelling ticket: %v", err))
				return
			}
			b.sendPrivmsg(target, fmt.Sprintf("Ticket %s cancelled.", ticketID))
		}
		return
	}

	// Paste command
	if strings.HasPrefix(message, b.prefix+"paste") {
		if b.uploadsDB == nil {
			b.sendPrivmsg(target, "Pastes system not initialized.")
			return
		}
		token := generateToken(16)
		err := b.uploadsDB.CreateUploadSession(token, sender, target)
		if err != nil {
			b.sendPrivmsg(target, fmt.Sprintf("Error creating paste session: %v", err))
			return
		}
		uploadURL := fmt.Sprintf("%s/upload?token=%s", b.cfg.Web.BaseURL, token)
		b.sendPrivmsg(sender, fmt.Sprintf("Paste requested. Fill the form here (expires in 30m): %s", uploadURL))
		b.sendPrivmsg(target, fmt.Sprintf("@%s: I've sent you a private message with the paste link.", sender))
		return
	}

	// File upload command
	if strings.HasPrefix(message, b.prefix+"upload") {
		if b.uploadsDB == nil {
			b.sendPrivmsg(target, "Uploads system not initialized.")
			return
		}
		token := generateToken(16)
		err := b.uploadsDB.CreateFileUploadSession(token, sender, target)
		if err != nil {
			b.sendPrivmsg(target, fmt.Sprintf("Error creating upload session: %v", err))
			return
		}
		uploadURL := fmt.Sprintf("%s/upload?token=%s", b.cfg.Web.BaseURL, token)
		b.sendPrivmsg(sender, fmt.Sprintf("File upload requested. Upload here (expires in 30m): %s", uploadURL))
		b.sendPrivmsg(target, fmt.Sprintf("@%s: I've sent you a private message with the upload link.", sender))
		return
	}

	downloadCmd := b.prefix + "download"
	if strings.HasPrefix(message, downloadCmd) {
		suf := strings.TrimPrefix(message, downloadCmd)
		if suf == "" || strings.HasPrefix(suf, " ") {
			if b.uploadsDB == nil {
				b.sendPrivmsg(target, "Uploads system not initialized.")
				return
			}
			const maxDownloadList = 100
			limit := maxDownloadList
			parts := strings.Fields(message)
			if len(parts) > 1 {
				n, err := strconv.Atoi(parts[1])
				if err != nil || n <= 0 {
					b.sendPrivmsg(target, fmt.Sprintf("Usage: %sdownload [N] — list your approved file uploads (newest first); N = last N files (max %d).", b.prefix, maxDownloadList))
					return
				}
				if n > maxDownloadList {
					n = maxDownloadList
				}
				limit = n
			}
			files, err := b.uploadsDB.ListApprovedFilesByUser(sender, limit)
			if err != nil {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: Error listing uploads: %v", sender, err))
				return
			}
			if len(files) == 0 {
				b.sendPrivmsg(target, fmt.Sprintf("@%s: No approved file uploads found.", sender))
				return
			}
			b.sendPrivmsg(target, fmt.Sprintf("@%s: %d file(s) (newest first):", sender, len(files)))
			for _, u := range files {
				name := u.OriginalFilename
				if name == "" {
					name = u.Title
				}
				if name == "" {
					name = u.TicketID
				}
				url := fmt.Sprintf("%s/f/%s", b.cfg.Web.BaseURL, u.TicketID)
				b.sendPrivmsg(target, fmt.Sprintf("  %s — %s", name, url))
				time.Sleep(1 * time.Second)
			}
			return
		}
	}

	// Handle !ask command
	if strings.HasPrefix(message, b.prefix+b.cmdName) {
		// Check rate limiting if enabled
		if b.rateLimiter != nil && !b.rateLimiter.Allow(sender, target, b.cfg.Bot.RateLimiting.Limit, b.cfg.Bot.RateLimiting.Burst) {
			if b.cfg.Bot.Debug {
				log.Printf("[DEBUG] Rate limited - Sender: %s, Target: %s", sender, target)
			}
			b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: Rate limit exceeded. Please wait before sending more commands.", sender)))
			return
		}

		if b.cfg.Bot.Debug {
			log.Printf("[DEBUG] Command detected - Target: %s, Question: %s, Sender: %s\n", target, message, sender)
		}

		question := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+b.cmdName))
		if question == "" {
			return
		}

		// Use a background context for the AI request
		ctx := context.Background()

		// Track request
		b.statsMu.Lock()
		b.aiRequests++
		b.statsMu.Unlock()
		if b.tracker != nil {
			b.tracker.LogAIRequest()
		}

		// Get response from AI
		response, err := b.aiClient.Ask(ctx, question)
		if err != nil {
			if b.cfg.Bot.Debug {
				log.Printf("Error contacting AI: %v\n", err)
			}
			b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("Error contacting AI: %v", err)))
			return
		}

		// Format the response to mention the sender and sanitize it for IRC compatibility
		formattedResponse := b.sanitize(fmt.Sprintf("@%s: %s", sender, response))

		// Handle long responses by truncating to prevent IRC limits (512 bytes max)
		if len(formattedResponse) > 500 {
			// Truncate to prevent exceeding 520-byte IRC limit (including prefixing)
			if len(formattedResponse) > 520 {
				formattedResponse = formattedResponse[:517] + "..."
			}
		}

		b.sendPrivmsg(target, formattedResponse)
	}
}

// handleCTCPRequest handles CTCP requests and sends appropriate responses via NOTICE.
func (b *Bot) handleCTCPRequest(sender, target, content string) {
	parts := strings.Fields(content)
	if len(parts) == 0 {
		return
	}

	command := strings.ToUpper(parts[0])

	if b.cfg.Bot.Debug {
		log.Printf("[DEBUG] CTCP Request - Sender: %s, Command: %s", sender, command)
	}

	switch command {
	case "VERSION":
		response := fmt.Sprintf("\x01VERSION botIAask:%s:Go/ergochat\x01", b.version)
		b.conn.Notice(sender, response)
	case "TIME":
		response := fmt.Sprintf("\x01TIME %s\x01", time.Now().Format(time.RFC1123))
		b.conn.Notice(sender, response)
	case "UPTIME":
		response := fmt.Sprintf("\x01UPTIME %s\x01", b.GetUptime())
		b.conn.Notice(sender, response)
	}
}

// NotifyAdmins sends a private message to all logged-in administrators.
func (b *Bot) NotifyAdmins(message string) {
	b.loginsMu.RLock()
	defer b.loginsMu.RUnlock()
	for nick := range b.loggedInAdmins {
		b.sendPrivmsg(nick, message)
	}
}

// NotifyLoggedInAdminsNotice sends a NOTICE to every admin in an active !admin session.
func (b *Bot) NotifyLoggedInAdminsNotice(message string) {
	if b.conn == nil || !b.IsConnected() {
		return
	}
	b.loginsMu.RLock()
	nicks := make([]string, 0, len(b.loggedInAdmins))
	for nick := range b.loggedInAdmins {
		nicks = append(nicks, nick)
	}
	b.loginsMu.RUnlock()
	msg := b.sanitize(message)
	for _, nick := range nicks {
		b.sendNotice(nick, msg)
	}
}

// SendMessage sends a message to a channel or user (used by web server).
func (b *Bot) SendMessage(target, message string) {
	b.sendPrivmsg(target, message)
}

// sendPrivmsg wraps conn.Privmsg and also logs the bot's own outbound messages
func (b *Bot) sendPrivmsg(target, message string) {
	b.conn.Privmsg(target, message)
	logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventMessage, b.cfg.IRC.Nickname, message, "")
}

// ircTextBudget is the byte cap passed to ircutils.SanitizeText in sanitize()
const ircTextBudget = 450

// sendPrivmsgMentionedLines sends PRIVMSGs of the form "@sender: " + each chunk of
// the given logical message parts, splitting long parts on word boundaries to stay
// within ircTextBudget.
func (b *Bot) sendPrivmsgMentionedLines(target, sender string, parts ...string) {
	mention := fmt.Sprintf("@%s: ", sender)
	prefix := len([]byte(mention))
	if prefix > 200 {
		prefix = 200
	}
	budget := ircTextBudget - prefix
	if budget < 64 {
		budget = 64
	}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		for _, chunk := range splitUTF8ByByteBudget(p, budget) {
			if chunk == "" {
				continue
			}
			b.sendPrivmsg(target, b.sanitize(mention+chunk))
		}
	}
}

// splitUTF8ByByteBudget splits s into substrings, each of byte length at most max, without breaking runes.
func splitUTF8ByByteBudget(s string, max int) []string {
	if max < 1 {
		return nil
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if len([]byte(s)) <= max {
		return []string{s}
	}
	var res []string
	words := strings.Fields(s)
	var cur string
	flush := func() {
		t := strings.TrimSpace(cur)
		if t != "" {
			res = append(res, t)
		}
		cur = ""
	}
	for _, w := range words {
		if cur == "" {
			if len([]byte(w)) > max {
				flush()
				for _, p := range splitStringByRunesToByteBudget(w, max) {
					res = append(res, p)
				}
				continue
			}
			cur = w
			continue
		}
		trial := cur + " " + w
		if len([]byte(trial)) <= max {
			cur = trial
		} else {
			flush()
			if len([]byte(w)) > max {
				for _, p := range splitStringByRunesToByteBudget(w, max) {
					res = append(res, p)
				}
			} else {
				cur = w
			}
		}
	}
	flush()
	return res
}

// splitStringByRunesToByteBudget splits a single token (no spaces) to fit the byte cap.
func splitStringByRunesToByteBudget(s string, max int) []string {
	if max < 1 {
		return nil
	}
	if len([]byte(s)) <= max {
		return []string{s}
	}
	var res []string
	rest := s
	for len([]byte(rest)) > max {
		b := []byte(rest)
		i := 0
		for i < len(b) {
			_, w := utf8.DecodeRune(b[i:])
			if w == 0 {
				break
			}
			if len(b[:i+w]) > max {
				break
			}
			i += w
		}
		if i < 1 {
			_, w := utf8.DecodeRune(b)
			if w < 1 {
				w = 1
			}
			i = w
		}
		res = append(res, string(b[:i]))
		rest = string(b[i:])
	}
	rest = strings.TrimSpace(rest)
	if rest != "" {
		res = append(res, rest)
	}
	return res
}

// sendNotice wraps conn.Notice and logs outbound notices (e.g. join reminders).
func (b *Bot) sendNotice(target, message string) {
	b.conn.Notice(target, message)
	logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventNotice, b.cfg.IRC.Nickname, message, "")
}

// notifyLoggedInAdminsPendingApprovals sends each logged-in admin a NOTICE with paste/file queue depth.
func (b *Bot) notifyLoggedInAdminsPendingApprovals(recipients []string) {
	msg := "Pending approvals: (uploads unavailable)."
	if b.uploadsDB != nil {
		pastes, err1 := b.uploadsDB.CountPendingPastes()
		files, err2 := b.uploadsDB.CountPendingFiles()
		if err1 != nil || err2 != nil {
			log.Printf("pending counts for admin NOTICE: %v %v", err1, err2)
			msg = "Pending approvals: could not read queue."
		} else {
			msg = fmt.Sprintf("Pending approvals: %d paste(s), %d file upload(s).", pastes, files)
		}
	}
	msg = b.sanitize(msg)
	for _, nick := range recipients {
		b.sendNotice(nick, msg)
	}
}

func truncateReminderNotice(s string, maxBytes int) string {
	if maxBytes <= 3 {
		return "..."
	}
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes-3] + "..."
}

// formatDuration formats a time.Duration into a human-readable string.
func formatDuration(d time.Duration) string {
	// Calculate hours, minutes, and seconds
	hours := int64(d.Hours())
	minutes := int64(d.Minutes()) % 60
	seconds := int64(d.Seconds()) % 60

	// Format the duration
	if hours > 0 {
		return fmt.Sprintf("%dh%dm%ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	} else {
		return fmt.Sprintf("%ds", seconds)
	}
}

func statsOnOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func statsIntOrNA(v int) string {
	if v < 0 {
		return "n/a"
	}
	return strconv.Itoa(v)
}

// sendAdminStats sends a multi-line snapshot for !stats (logged-in admins only).
func (b *Bot) sendAdminStats(target, sender string) {
	b.statsMu.Lock()
	aiN := b.aiRequests
	b.statsMu.Unlock()

	appU := formatDuration(time.Since(b.startTime))
	sessU := formatDuration(time.Since(b.connectionTime))

	nCh := len(b.cfg.IRC.Channels)
	nGo := runtime.NumGoroutine()

	b.ignoreMu.RLock()
	nIgn := len(b.ignoreList)
	b.ignoreMu.RUnlock()

	b.loginsMu.RLock()
	nAdm := len(b.loggedInAdmins)
	b.loginsMu.RUnlock()

	snap := "off"
	if b.tracker != nil && b.tracker.IsEnabled() {
		snap = "on"
	}

	line1 := fmt.Sprintf("Bot %s | IRC=%s | chans=%d | go=%d | ign=%d | AI=%d | up app=%s sess=%s | activity_snap=%s",
		meta.Version, statsOnOff(b.IsConnected()), nCh, nGo, nIgn, aiN, appU, sessU, snap)

	pending, bookm, rem, pastes, files := -1, -1, -1, -1, -1
	if b.uploadsDB != nil {
		if n, err := b.uploadsDB.CountPendingApproval(); err != nil {
			log.Printf("stats: pending uploads: %v", err)
		} else {
			pending = n
		}
		if n, err := b.uploadsDB.CountApprovedPastes(); err != nil {
			log.Printf("stats: paste count: %v", err)
		} else {
			pastes = n
		}
		if n, err := b.uploadsDB.CountApprovedFiles(); err != nil {
			log.Printf("stats: file count: %v", err)
		} else {
			files = n
		}
	}
	if b.bookmarksDB != nil {
		if n, err := b.bookmarksDB.GetBookmarksCount(""); err != nil {
			log.Printf("stats: bookmarks: %v", err)
		} else {
			bookm = n
		}
		if n, err := b.bookmarksDB.CountReminders(); err != nil {
			log.Printf("stats: reminders: %v", err)
		} else {
			rem = n
		}
	}

	line2 := fmt.Sprintf("Queue=%s | bkm=%s | rem=%s | paste=%s | file=%s | admins=%d",
		statsIntOrNA(pending), statsIntOrNA(bookm), statsIntOrNA(rem), statsIntOrNA(pastes), statsIntOrNA(files), nAdm)

	newsDB := "?"
	if b.rssDB != nil {
		if n, err := b.rssDB.CountSeenNews(); err != nil {
			log.Printf("stats: news db: %v", err)
		} else {
			newsDB = strconv.Itoa(n)
		}
	}
	line3 := fmt.Sprintf("RSS=%s, retain=%d, announce=%s, newsDB=%s rows",
		statsOnOff(b.cfg.RSS.Enabled), b.cfg.RSS.RetentionCount, statsOnOff(b.cfg.RSS.AnnounceToIRCEnabled()), newsDB)

	host := sysinfo.Collect(400 * time.Millisecond)
	ramPart := "RAM=n/a"
	if host.RAMAvailable != "" {
		ramPart = fmt.Sprintf("RAM avail=%s (%.1f%% used)", host.RAMAvailable, host.RAMUsedPct)
	}
	cpuPart := "CPU=n/a"
	if host.CPUValid {
		cpuPart = fmt.Sprintf("CPU=%.1f%%", host.CPUPct)
	}
	line4 := fmt.Sprintf("%s/%s | %s | %s", runtime.GOOS, runtime.GOARCH, ramPart, cpuPart)

	b.sendPrivmsgMentionedLines(target, sender, line1, line2, line3, line4)
}

// sanitize cleans a string for IRC compatibility using ircutils.
func (b *Bot) sanitize(s string) string {
	// Use 512 as the standard IRC message limit (including overhead).
	// We use a slightly smaller limit for the text content itself to allow for prefixing.
	return ircutils.SanitizeText(s, ircTextBudget)
}

func (b *Bot) updateTrackerAdmins() {
	if b.tracker == nil {
		return
	}

	b.loginsMu.RLock()
	admins := make([]string, 0, len(b.loggedInAdmins))
	for nick := range b.loggedInAdmins {
		admins = append(admins, nick)
	}
	b.loginsMu.RUnlock()

	b.membersMu.RLock()
	chanPresence := make(map[string][]string)
	for channel, members := range b.channelMembers {
		chanAdmins := []string{}
		for nick := range members {
			b.loginsMu.RLock()
			loggedIn := b.loggedInAdmins[nick]
			b.loginsMu.RUnlock()
			if loggedIn {
				chanAdmins = append(chanAdmins, nick)
			}
		}
		if len(chanAdmins) > 0 {
			chanPresence[channel] = chanAdmins
		}
	}
	b.membersMu.RUnlock()

	b.tracker.UpdateAdminData(admins, chanPresence)
}

// RateLimiter implements rate limiting for IRC commands
type RateLimiter struct {
	mu     sync.RWMutex
	limits map[string]*UserRateLimit
	window time.Duration
}

// UserRateLimit tracks rate limits for a specific user in a specific channel
type UserRateLimit struct {
	lastReset time.Time
	counts    map[string]int // channel -> count
}

// NewRateLimiter creates a new rate limiter with the given window
func NewRateLimiter(window time.Duration) *RateLimiter {
	return &RateLimiter{
		limits: make(map[string]*UserRateLimit),
		window: window,
	}
}

// Allow checks if a command is allowed under rate limiting rules
func (rl *RateLimiter) Allow(sender, target string, limit, burst int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	key := fmt.Sprintf("%s:%s", sender, target)
	now := time.Now()

	userLimit, exists := rl.limits[key]
	if !exists {
		userLimit = &UserRateLimit{
			lastReset: now,
			counts:    make(map[string]int),
		}
		rl.limits[key] = userLimit
	}

	if now.Sub(userLimit.lastReset) > rl.window {
		userLimit.lastReset = now
		userLimit.counts[target] = 0
	}

	userLimit.counts[target]++
	return userLimit.counts[target] <= burst
}

func generateToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
