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

	// Rate limiting fields
	rateLimiter    *RateLimiter
}

// RateLimiter implements rate limiting for IRC commands
type RateLimiter struct {
	mu        sync.RWMutex
	limits    map[string]*UserRateLimit
	window    time.Duration
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

// Allow checks if a command from user in channel is allowed
func (rl *RateLimiter) Allow(user, channel string, limit, burst int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	key := user + ":" + channel

	// Initialize or update the user rate limit
	userLimit, exists := rl.limits[key]
	if !exists {
		userLimit = &UserRateLimit{
			lastReset: now,
			counts:    make(map[string]int),
		}
		rl.limits[key] = userLimit
	}

	// Check if we need to reset the counter based on window
	if now.Sub(userLimit.lastReset) >= rl.window {
		userLimit.lastReset = now
		for k := range userLimit.counts {
			delete(userLimit.counts, k)
		}
	}

	// Check if the user has exceeded the burst limit
	currentCount := userLimit.counts[channel]
	if currentCount >= burst {
		return false
	}

	// Check if the user has exceeded the regular limit within the window
	if currentCount >= limit {
		return false
	}

	// Allow the command and increment the count
	userLimit.counts[channel]++
	return true
}

// RemoveExpired removes expired rate limit entries to prevent memory leaks
func (rl *RateLimiter) RemoveExpired() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for key, userLimit := range rl.limits {
		if now.Sub(userLimit.lastReset) >= rl.window*2 {
			delete(rl.limits, key)
		}
	}
}

// InitializeRateLimiter initializes the rate limiter based on configuration
func (b *Bot) InitializeRateLimiter() {
	if b.cfg.Bot.RateLimiting != nil && b.cfg.Bot.RateLimiting.Enabled {
		window := time.Duration(b.cfg.Bot.RateLimiting.Window) * time.Second
		b.rateLimiter = NewRateLimiter(window)
	} else {
		b.rateLimiter = nil
	}
}

// NewBot creates a new IRC bot instance.
func NewBot(cfg *config.Config, aiClient *ai.Client) *Bot {
	bot := &Bot{
		cfg:            cfg,
		aiClient:       aiClient,
		prefix:         cfg.Bot.CommandPrefix,
		cmdName:        cfg.Bot.CommandName,
		adminEnabled:   true,
		startTime:      time.Now(),
	}

	// Initialize rate limiter
	bot.InitializeRateLimiter()

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
			b.conn.Join(channel)
		}
	})

	// Handle PRIVMSG (messages in channels or private)
	b.conn.AddCallback("PRIVMSG", func(e ircmsg.Message) {
		target := e.Params[0] // Channel or Nick
		message := e.Params[1]
		sender := e.Nick()

		// Note: e.Prefix is undefined in the current version of ircmsg.Message.
		// We are relying on the sender's nick for now.

		if b.cfg.Bot.Debug {
			log.Printf("[DEBUG] PRIVMSG received - Sender: %s, Target: %s, Content: %s", sender, target, message)
		}

		b.handleCommand(target, message, sender)
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

// handleCommand checks for the !ask and !toggle command and interacts with the AI client.
func (b *Bot) handleCommand(target, message, sender string) {
	// Handle !uptime command
	if strings.HasPrefix(message, b.prefix+"uptime") {
		// Calculate uptime durations
		appUptime := time.Since(b.startTime)
		sessionUptime := time.Since(b.connectionTime)

		// Format durations
		appUptimeStr := formatDuration(appUptime)
		sessionUptimeStr := formatDuration(sessionUptime)

		// Send response to IRC
		b.conn.Privmsg(target, b.sanitize(fmt.Sprintf("Bot uptime: App=%s, Session=%s", appUptimeStr, sessionUptimeStr)))
		return
	}

	// Handle !ask command
	if strings.HasPrefix(message, b.prefix+b.cmdName) {
		// Check rate limiting if enabled
		if b.rateLimiter != nil && !b.rateLimiter.Allow(sender, target, b.cfg.Bot.RateLimiting.Limit, b.cfg.Bot.RateLimiting.Burst) {
			if b.cfg.Bot.Debug {
				log.Printf("[DEBUG] Rate limited - Sender: %s, Target: %s", sender, target)
			}
			b.conn.Privmsg(target, b.sanitize(fmt.Sprintf("@%s: Rate limit exceeded. Please wait before sending more commands.", sender)))
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
			b.conn.Privmsg(target, b.sanitize(fmt.Sprintf("Error contacting AI: %v", err)))
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

		b.conn.Privmsg(target, formattedResponse)
	}
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
// It removes null bytes, converts newlines to spaces, and ensures UTF-8 safe truncation.
func (b *Bot) sanitize(s string) string {
	// Use 512 as the standard IRC message limit (including overhead).
	// We use a slightly smaller limit for the text content itself to allow for prefixing.
	return ircutils.SanitizeText(s, 450)
}