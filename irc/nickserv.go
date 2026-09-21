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

const capAccountReg = "draft/account-registration"

// NickServCommand runs an account action on the named network and returns the server's
// replies (waits ~3s). ok is true when the server confirmed account creation/verification.
// register uses the IRCv3 REGISTER command when the network offers draft/account-registration
// (networks without NickServ), else "NickServ REGISTER". verify (IRCv3 only) confirms an emailed
// code. Dashboard-only by design.
func (b *Bot) NickServCommand(network, action, password, email, code string) (replies []string, ok bool, err error) {
	n := b.network(network)
	if n == nil {
		return nil, false, fmt.Errorf("unknown network %q", network)
	}
	n.statsMu.Lock()
	connected := n.connected
	n.statsMu.Unlock()
	if !connected {
		return nil, false, fmt.Errorf("network %q is not connected", network)
	}
	// Reject anything that could inject extra IRC parameters or lines.
	bad := func(v string) bool { return v == "" || strings.ContainsAny(v, " \r\n\x00") }
	_, ircv3 := n.conn.AcknowledgedCaps()[capAccountReg]
	nick := n.netCfg().Nickname

	var send func() error
	switch action {
	case "identify":
		if bad(password) {
			return nil, false, fmt.Errorf("password is required and must not contain spaces or control characters")
		}
		send = func() error { return n.conn.Privmsg("NickServ", "IDENTIFY "+password) }
	case "register":
		if bad(password) {
			return nil, false, fmt.Errorf("password is required and must not contain spaces or control characters")
		}
		if ircv3 {
			if email == "" {
				email = "*" // no email (only if the network doesn't require one)
			} else if bad(email) {
				return nil, false, fmt.Errorf("invalid email")
			}
			send = func() error { return n.conn.Send("REGISTER", "*", email, password) }
		} else {
			if bad(email) {
				return nil, false, fmt.Errorf("a valid email is required to register")
			}
			send = func() error { return n.conn.Privmsg("NickServ", "REGISTER "+password+" "+email) }
		}
	case "verify":
		if !ircv3 {
			return nil, false, fmt.Errorf("network does not support IRCv3 account registration; verify via NickServ from another client")
		}
		if bad(code) {
			return nil, false, fmt.Errorf("verification code required (no spaces)")
		}
		send = func() error { return n.conn.Send("VERIFY", nick, code) }
	default:
		return nil, false, fmt.Errorf("unknown action %q", action)
	}
	n.nsMu.Lock()
	seq := n.nsSeq
	n.nsMu.Unlock()
	if err := send(); err != nil {
		return nil, false, err
	}
	time.Sleep(3 * time.Second)
	replies = n.nickServSince(seq)
	for _, r := range replies {
		if strings.HasPrefix(r, "REGISTER SUCCESS") || strings.HasPrefix(r, "VERIFY SUCCESS") {
			ok = true
		}
	}
	return replies, ok, nil
}
