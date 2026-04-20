package irc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"botIAask/ai"
	"botIAask/bookmarks"
	"botIAask/config"
	"botIAask/crypto"
	"botIAask/logger"
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
		version:        "0.2.1",
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

// SetRSSDatabase sets the RSS database for the bot
func (b *Bot) SetRSSDatabase(db *rss.Database) {
	b.rssDB = db
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

// GetUptime returns the human-readable uptime of the bot.
func (b *Bot) GetUptime() string {
	return formatDuration(time.Since(b.startTime))
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

// Reload updates the bot's configuration.
func (b *Bot) Reload(cfg *config.Config) {
	b.membersMu.Lock()
	defer b.membersMu.Unlock()
	b.cfg = cfg
	b.prefix = cfg.Bot.CommandPrefix
	b.cmdName = cfg.Bot.CommandName
	log.Printf("Bot configuration reloaded.")
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
			if b.cfg.Bot.Debug {
				log.Printf("[DEBUG] Joining channel: %s", channel)
			}
			b.conn.Join(channel)
		}
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
			logger.LogChannelEvent(b.cfg.IRC.Server, channel, logger.EventQuit, sender, message, "")
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
			logger.LogChannelEvent(b.cfg.IRC.Server, channel, logger.EventNick, sender, newNick, "")
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

	// Connect and run the event loop
	err := b.conn.Connect()
	if err != nil {
		return fmt.Errorf("failed to connect to IRC server: %w", err)
	}

	// The Loop handles reconnection automatically
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
		helpMsg := fmt.Sprintf("Commands: %s%s <query>, %sbc <expr>, %snews [limit], %sbookmark <URL> [nickname], %suptime, %sspec, %spaste, %supload, %sdownload [N], %seuro, %speso, %scrypto", 
			b.prefix, b.cmdName, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix)
		if isAdmin && isLoggedInAdmin {
			helpMsg += fmt.Sprintf(" | Admin: %sadmin off, %sjoin #chan, %spart #chan, %signore nick, %sstats, %ssay #chan msg, %squit msg, %snews on/off, %sop [nick], %sdeop [nick], %svoice [nick], %sdevoice [nick], %sticket pending/approve/cancel [ID]", 
				b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix, b.prefix)
		} else if isAdmin {
			helpMsg += fmt.Sprintf(" | Admin: Auth required using %sadmin", b.prefix)
		}
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s", sender, helpMsg)))
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
				b.loginsMu.Lock()
				b.loggedInAdmins[sender] = true
				b.loginsMu.Unlock()
				if b.tracker != nil {
					b.updateTrackerAdmins()
				}
				b.sendPrivmsg(target, fmt.Sprintf("%s logged in to admin session.", sender))
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
			channel := strings.TrimSpace(strings.TrimPrefix(message, b.prefix+"join "))
			if channel != "" {
				b.conn.Join(channel)
				if b.tracker != nil {
					b.tracker.LogAdminCommand()
				}
				b.sendPrivmsg(target, fmt.Sprintf("Joining %s...", channel))
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
			b.statsMu.Lock()
			count := b.aiRequests
			b.statsMu.Unlock()
			if b.tracker != nil {
				b.tracker.LogAdminCommand()
			}
			b.sendPrivmsg(target, fmt.Sprintf("Stats: AI Requests=%d, Uptime=%s", count, b.GetUptime()))
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
			if reason == "" {
				reason = "Shutting down"
			}
			if b.tracker != nil {
				b.tracker.LogAdminCommand()
			}
			b.conn.QuitMessage = reason
			b.conn.Quit()
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
			adminCmds := []string{"join", "part", "ignore", "stats", "say", "quit", "op", "deop", "voice", "devoice"}
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
			
			msg := fmt.Sprintf("\x0304,01[NEWS]\x03 %s - %s", e.PubDate.Format("15:04"), e.Title)
			if displayLink != "" {
				msg += fmt.Sprintf(" \x0312\x1f🔗\x1f\x03 %s", displayLink)
			}
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

		parts := strings.Fields(message)
		if len(parts) < 2 {
			b.sendPrivmsg(target, fmt.Sprintf("Usage: %sbookmark <URL> [nickname]", b.prefix))
			return
		}

		url := parts[1]
		nickname := ""
		if len(parts) > 2 {
			nickname = parts[2]
		}

		// Rate limiting: 3 within 10 minutes
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

		// Use sender's nickname if none provided
		if nickname == "" {
			nickname = sender
		}

		// Get hostname of the user (from source)
		hostname := "unknown"
		if idx := strings.Index(source, "@"); idx != -1 {
			hostname = source[idx+1:]
		}

		id, err := b.bookmarksDB.AddBookmark(url, nickname, hostname)
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

// SendMessage sends a message to a channel or user (used by web server).
func (b *Bot) SendMessage(target, message string) {
	b.sendPrivmsg(target, message)
}

// sendPrivmsg wraps conn.Privmsg and also logs the bot's own outbound messages
func (b *Bot) sendPrivmsg(target, message string) {
	b.conn.Privmsg(target, message)
	logger.LogChannelEvent(b.cfg.IRC.Server, target, logger.EventMessage, b.cfg.IRC.Nickname, message, "")
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

// sanitize cleans a string for IRC compatibility using ircutils.
func (b *Bot) sanitize(s string) string {
	// Use 512 as the standard IRC message limit (including overhead).
	// We use a slightly smaller limit for the text content itself to allow for prefixing.
	return ircutils.SanitizeText(s, 450)
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
