package irc

import (
	"fmt"
	"strings"
	"time"
)

const nsReplyCap = 20

// recordNickServ keeps the latest NickServ notices so the dashboard can show the outcome
// of a register/identify. Passwords are never in these (they're server replies).
func (n *ircNetwork) recordNickServ(msg string) {
	n.nsMu.Lock()
	n.nsReplies = append(n.nsReplies, msg)
	if len(n.nsReplies) > nsReplyCap {
		n.nsReplies = n.nsReplies[len(n.nsReplies)-nsReplyCap:]
	}
	n.nsSeq++
	n.nsMu.Unlock()
}

func (n *ircNetwork) nickServSince(seq int) []string {
	n.nsMu.Lock()
	defer n.nsMu.Unlock()
	fresh := n.nsSeq - seq
	if fresh <= 0 {
		return nil
	}
	if fresh > len(n.nsReplies) {
		fresh = len(n.nsReplies)
	}
	return append([]string(nil), n.nsReplies[len(n.nsReplies)-fresh:]...)
}

// NickServCommand sends "REGISTER <password> <email>" or "IDENTIFY <password>" to NickServ on
// the named network and returns NickServ's replies (waits ~3s). Dashboard-only by design.
func (b *Bot) NickServCommand(network, action, password, email string) ([]string, error) {
	n := b.network(network)
	if n == nil {
		return nil, fmt.Errorf("unknown network %q", network)
	}
	n.statsMu.Lock()
	connected := n.connected
	n.statsMu.Unlock()
	if !connected {
		return nil, fmt.Errorf("network %q is not connected", network)
	}
	// Reject anything that could inject extra IRC parameters or lines.
	if password == "" || strings.ContainsAny(password, " \r\n\x00") {
		return nil, fmt.Errorf("password is required and must not contain spaces or control characters")
	}
	var line string
	switch action {
	case "identify":
		line = "IDENTIFY " + password
	case "register":
		if email == "" || strings.ContainsAny(email, " \r\n\x00") {
			return nil, fmt.Errorf("a valid email is required to register")
		}
		line = "REGISTER " + password + " " + email
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
	n.nsMu.Lock()
	seq := n.nsSeq
	n.nsMu.Unlock()
	if err := n.conn.Privmsg("NickServ", line); err != nil {
		return nil, err
	}
	time.Sleep(3 * time.Second)
	return n.nickServSince(seq), nil
}
