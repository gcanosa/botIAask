// Package ircusage prints a terminal-friendly IRC command reference for the bot CLI.
package ircusage

import (
	"fmt"
	"io"
	"os"

	"botIAask/meta"
	"golang.org/x/term"
)

// UseColorForStdout returns true if output should use ANSI (TTY and NO_COLOR unset).
func UseColorForStdout() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

type line struct {
	cmd, desc string
}

// User command rows (prefix "!" is default; configurable in config).
var userLines = []line{
	{cmd: "!ask <query>", desc: "Ask the AI (name configurable; default ask)"},
	{cmd: "!bc <expr>", desc: "Evaluate a math expression, e.g. 5+5"},
	{cmd: "!weather <place>", desc: "Current + 5-day forecast (e.g. Barcelona, Spain)"},
	{cmd: "!news [limit]", desc: "Fetch recent RSS items (in channels with news enabled; limit 1–10)"},
	{cmd: "!bookmark ...", desc: "ADD <URL> [nickname] | FIND <text>"},
	{cmd: "!uptime", desc: "Show bot and session uptime"},
	{cmd: "!spec", desc: "Show system prompt spec"},
	{cmd: "!paste", desc: "Get a link to upload a text paste"},
	{cmd: "!upload", desc: "Get a link to upload a file (max size in web settings)"},
	{cmd: "!download [N]", desc: "List your approved uploads with URLs (newest first; optional last N)"},
	{cmd: "!euro", desc: "Euro / forex rate view"},
	{cmd: "!peso", desc: "Argentine peso rate view"},
	{cmd: "!crypto", desc: "Crypto market view"},
	{cmd: "!reminder ...", desc: "add <note> | del <id> | list | read <id>"},
	{cmd: "!todo ...", desc: "add <text> (public web) | list | del <id> | private <text> (admins: staff-only)"},
	{cmd: "!help", desc: "Short command list in the channel"},
}

// Admin command rows (hostmask + !admin session).
var adminLines = []line{
	{cmd: "!admin", desc: "Log in to an admin session (hostmask must match config)"},
	{cmd: "!admin off", desc: "Log out of admin session"},
	{cmd: "!join #channel [key]", desc: "Join a channel; optional +k key (saved in config)"},
	{cmd: "!part [#channel]", desc: "Leave a channel (updates config when applicable)"},
	{cmd: "!ignore <nick>", desc: "Ignore a user"},
	{cmd: "!say #chan <msg>", desc: "Send a message to a channel"},
	{cmd: "!news on|off", desc: "Toggle news for the current channel (session only)"},
	{cmd: "!news start|stop", desc: "Turn global RSS-to-IRC announcements on/off (saves config)"},
	{cmd: "!stats", desc: "Bot stats: IRC/data queues, RSS config vs news DB rows, host RAM/CPU, uptime"},
	{cmd: "!op [nick]", desc: "Give channel operator to self or nick (in channel)"},
	{cmd: "!deop [nick]", desc: "Remove channel operator (in channel)"},
	{cmd: "!voice [nick]", desc: "Give voice to self or nick (in channel)"},
	{cmd: "!devoice [nick]", desc: "Remove voice (in channel)"},
	{cmd: "!ticket ...", desc: "pending | approve <ID> | cancel <ID>"},
	{cmd: "!rehash", desc: "Reload config from disk (notifies other admins)"},
	{cmd: "!quit [reason]", desc: "Disconnect (default message from irc.quit_message or app meta)"},
}

// ansi
const (
	reset     = "\033[0m"
	bold      = "\033[1m"
	dim       = "\033[2m"
	cyan      = "\033[36m"
	cyanHi    = "\033[1;36m"
	yellow    = "\033[33m"
	yellowHi  = "\033[1;33m"
	magentaHi = "\033[1;35m"
)

// Fprint writes the full IRC command reference. When color is false, output is plain text.
func Fprint(w io.Writer, color bool) {
	if color {
		fmt.Fprintf(w, "%s%s v%s — IRC command reference%s\n", bold, meta.Name, meta.Version, reset)
		fmt.Fprint(w, dim, "Command prefix and AI trigger name are configurable in config (defaults: ", cyanHi, "!", reset+dim, " and ", cyanHi, "ask", reset+dim, ").\n\n")
		fmt.Fprintf(w, "%s── User commands ──%s\n", bold+cyan, reset)
		for _, row := range userLines {
			fmt.Fprintf(w, "  %s%s%s  %s%s%s\n", cyan, row.cmd, reset, dim, row.desc, reset)
		}
		fmt.Fprintf(w, "\n%s── Admin commands ──%s\n", bold+magentaHi, reset)
		fmt.Fprint(w, dim, "Require matching hostmask in config and an active ", yellowHi, "!admin", reset+dim, " session.\n\n")
		for _, row := range adminLines {
			fmt.Fprintf(w, "  %s%s%s  %s%s%s\n", yellow, row.cmd, reset, dim, row.desc, reset)
		}
		fmt.Fprint(w, "\n")
		return
	}

	fmt.Fprintf(w, "%s v%s — IRC command reference\n", meta.Name, meta.Version)
	fmt.Fprintf(w, "Command prefix and AI trigger name are configurable in config (defaults: ! and ask).\n\n")
	fmt.Fprintln(w, "── User commands ──")
	for _, row := range userLines {
		fmt.Fprintf(w, "  %-30s  %s\n", row.cmd, row.desc)
	}
	fmt.Fprintln(w, "\n── Admin commands ──")
	fmt.Fprintln(w, "Require matching hostmask in config and an active !admin session.")
	fmt.Fprint(w, "\n")
	for _, row := range adminLines {
		fmt.Fprintf(w, "  %-30s  %s\n", row.cmd, row.desc)
	}
	fmt.Fprint(w, "\n")
}
