package irc

import (
	"fmt"
	"sort"
	"strings"

	"botIAask/internal/ircusage"
)

// helpNames renders sorted command names with the configured prefix; the AI trigger
// (listed as "ask" in ircusage) is shown under its configured name.
func (b *ircNetwork) helpNames(admin bool) string {
	names := ircusage.Names(admin)
	for i, n := range names {
		if n == "ask" {
			names[i] = b.cmd()
		}
	}
	sort.Strings(names)
	return b.pfx() + strings.Join(names, ", "+b.pfx())
}

// handleHelpCommand: "!help" lists commands alphabetically; "!help <cmd>" explains one.
func (b *ircNetwork) handleHelpCommand(target, sender, message string, isAdmin, isLoggedInAdmin bool) {
	f := strings.Fields(message)
	if len(f) > 1 {
		name := strings.ToLower(strings.TrimPrefix(f[1], b.pfx()))
		if name == strings.ToLower(b.cmd()) {
			name = "ask"
		}
		rows, intro, ok := ircusage.Lookup(name)
		if !ok {
			b.sendPrivmsgMentionedLines(target, sender, fmt.Sprintf("No such command: %s. Try %shelp for the list.", f[1], b.pfx()))
			return
		}
		lines := []string{}
		if intro != "" {
			lines = append(lines, intro)
		}
		for _, r := range rows {
			cmd := r.Cmd
			if name == "ask" {
				cmd = "!" + b.cmd() + strings.TrimPrefix(cmd, "!ask")
			}
			tag := ""
			if r.Admin {
				tag = " [admin]"
			}
			lines = append(lines, strings.ReplaceAll(cmd+tag+" — "+r.Desc, "!", b.pfx()))
		}
		b.sendPrivmsgMentionedLines(target, sender, lines...)
		return
	}
	public := fmt.Sprintf("Commands: %s | %shelp <command> for details", b.helpNames(false), b.pfx())
	switch {
	case isAdmin && isLoggedInAdmin:
		b.sendPrivmsgMentionedLines(target, sender, public, "Admin: "+b.helpNames(true))
	case isAdmin:
		b.sendPrivmsgMentionedLines(target, sender, public+fmt.Sprintf(" | Admin: auth required using %sadmin", b.pfx()))
	default:
		b.sendPrivmsgMentionedLines(target, sender, public)
	}
}
