package irc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"botIAask/ai"
	"botIAask/config"
	"botIAask/logger"

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
		version:        "1.0.0",
	}

	// Initialize rate limiter
	if cfg.Bot.RateLimiting != nil && cfg.Bot.RateLimiting.Enabled {
		window := time.Duration(cfg.Bot.RateLimiting.Window) * time.Second
		bot.rateLimiter = NewRateLimiter(window)
	}

	return bot
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
		RequestCaps: []string{"server-time", "message-tags"},
	}

	// Handle connection established event
	b.conn.AddConnectCallback(func(e ircmsg.Message) {
		log.Printf("Connected to %s! Joining channels...", serverAddr)
		b.connectionTime = time.Now()
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

		if strings.HasPrefix(message, "\x01ACTION ") && strings.HasSuffix(message, "\x01") {
			actionMsg := message[8 : len(message)-1]
			logger.LogChannelEvent(target, logger.EventAction, sender, actionMsg, "")
		} else {
			logger.LogChannelEvent(target, logger.EventMessage, sender, message, "")
			b.handleCommand(target, message, sender)
		}
	})

	b.conn.AddCallback("NOTICE", func(e ircmsg.Message) {
		if len(e.Params) < 2 {
			return
		}
		target := e.Params[0]
		message := e.Params[1]
		sender := e.Nick()
		logger.LogChannelEvent(target, logger.EventNotice, sender, message, "")
	})

	b.conn.AddCallback("JOIN", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		target := e.Params[0] // Channel
		sender := e.Nick()
		logger.LogChannelEvent(target, logger.EventJoin, sender, "", "")
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
		logger.LogChannelEvent(target, logger.EventPart, sender, message, "")
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
		logger.LogChannelEvent(target, logger.EventKick, sender, message, kicked)
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
			logger.LogChannelEvent(channel, logger.EventQuit, sender, message, "")
		}
	})

	b.conn.AddCallback("NICK", func(e ircmsg.Message) {
		if len(e.Params) < 1 {
			return
		}
		sender := e.Nick()
		newNick := e.Params[0]
		for _, channel := range b.cfg.IRC.Channels {
			logger.LogChannelEvent(channel, logger.EventNick, sender, newNick, "")
		}
	})

	// Handle disconnection events
	b.conn.AddDisconnectCallback(func(e ircmsg.Message) {
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

// handleCommand checks for the !ask, !uptime, and !spec command and interacts with the AI client.
func (b *Bot) handleCommand(target, message, sender string) {
	// Handle !uptime command
	if strings.HasPrefix(message, b.prefix+"uptime") {
		appUptime := time.Since(b.startTime)
		sessionUptime := time.Since(b.connectionTime)

		appUptimeStr := formatDuration(appUptime)
		sessionUptimeStr := formatDuration(sessionUptime)

		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("Bot uptime: App=%s, Session=%s", appUptimeStr, sessionUptimeStr)))
		return
	}

	// Handle !spec command (Restored)
	if strings.HasPrefix(message, b.prefix+"spec") {
		spec := "System Prompt: You are a helpful IRC bot. Keep responses concise and suitable for IRC."
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s", sender, spec)))
		return
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

// sendPrivmsg wraps conn.Privmsg and also logs the bot's own outbound messages
func (b *Bot) sendPrivmsg(target, message string) {
	b.conn.Privmsg(target, message)
	logger.LogChannelEvent(target, logger.EventMessage, b.cfg.IRC.Nickname, message, "")
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
