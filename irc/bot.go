package irc

import (
	"context"
	"fmt"
	"log"
	"strings"

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
}

// NewBot creates a new IRC bot instance.
func NewBot(cfg *config.Config, aiClient *ai.Client) *Bot {
	return &Bot{
		cfg:            cfg,
		aiClient:       aiClient,
		prefix:         cfg.Bot.CommandPrefix,
		cmdName:        cfg.Bot.CommandName,
		adminEnabled:   true,
		startTime:      time.Now(),
	}
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

		// Reconstruct identity (user@host) from the message Prefix
		identity := sender
		// Note: e.Prefix is undefined in the current version of ircmsg.Message.
		// We are relying on the sender's nick for now.

		if b.cfg.Bot.Debug {
			log.Printf("[DEBUG] PRIVMSG received - Sender: %s, Identity: %s, Target: %s, Content: %s\n", sender, identity, target, message)
		}

		b.handleCommand(target, message, sender, identity, e)
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
func (b *Bot) handleCommand(target, message, sender, identity string, e ircmsg.Message) {
	// Check if bot is disabled
	if !b.adminEnabled && !strings.HasPrefix(message, b.prefix+"toggle") {
		return
	}

	// Handle !toggle command
	if strings.HasPrefix(message, b.prefix+"toggle") {
		isAdmin := false
		// The sender's full identity (hostmask) is often available in the message tags or can be reconstructed.
		// For ergochat/irc-go, we check if the admin entry matches the nick or a part of the identity.
		// We're trying to match against the sender's nick or a reconstructed hostmask if possible.
		// A more robust way would be to use e.Params and look for user@host.
		// Since we want to support hostmasks like ~ethernet@user/ethernet:

		for _, admin := range b.cfg.Admin.Admins {
			// Check if the admin entry (which could be a hostmask) matches the sender's nick or reconstructed identity.
			if sender == admin || strings.Contains(admin, sender) || strings.Contains(identity, admin) || strings.HasPrefix(admin, "~") && strings.Contains(identity, strings.TrimPrefix(admin, "~")) {
				isAdmin = true
				break
			}
		}

		if !isAdmin {
			b.conn.Privmsg(target, b.sanitize(fmt.Sprintf("Access denied: %s is not an admin.", sender)))
			return
		}

		b.adminEnabled = !b.adminEnabled
		status := "enabled"
		if !b.adminEnabled {
			status = "disabled"
		}
		b.conn.Privmsg(target, b.sanitize(fmt.Sprintf("Bot command processing is now %s.", status)))
		return
	}

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

		// IRC has a limit on message length (usually 512 chars). We should split if necessary.
		if len(formattedResponse) > 400 {
			formattedResponse = formattedResponse[:397] + "..."
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
