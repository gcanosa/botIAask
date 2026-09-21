package irc

import (
	"fmt"
	"strings"
	"time"

	"github.com/ergochat/irc-go/ircmsg"
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

// recordServerReply captures server errors (4xx/5xx numerics), login numerics (900-908) and
// server NOTICEs, but only while a dashboard account action is waiting for its reply.
func (n *ircNetwork) recordServerReply(e ircmsg.Message) {
	n.nsMu.Lock()
	capturing := n.nsCapture
	n.nsMu.Unlock()
	if !capturing || len(e.Params) == 0 {
		return
	}
	c := e.Command
	numeric := len(c) == 3 && c[0] >= '0' && c[0] <= '9'
	switch {
	case c == "NOTICE" && !strings.Contains(e.Source, "!") && len(e.Params) >= 2:
		n.recordNickServ("NOTICE " + e.Params[len(e.Params)-1])
	case numeric && c != "401" && (c[0] == '4' || c[0] == '5' || c[0] == '9'):
		n.recordNickServ(c + " " + e.Params[len(e.Params)-1])
	}
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

const (
	capAccountReg      = "draft/account-registration"
	capAccountRegFinal = "account-registration"
)

func (n *ircNetwork) hasAccountReg() bool {
	caps := n.conn.AcknowledgedCaps()
	_, a := caps[capAccountReg]
	_, b := caps[capAccountRegFinal]
	return a || b
}

// NickServCommand runs an account action on the named network and returns the server's
// replies (waits ~3s). ok is true when the server confirmed account creation/verification.
// register uses the IRCv3 REGISTER command when the network offers draft/account-registration
// (networks without NickServ), else "NickServ REGISTER". verify (IRCv3 only) confirms an emailed
// code. Dashboard-only by design.
func (b *Bot) NickServCommand(network, action, method, password, email, code string) (replies []string, ok bool, err error) {
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
	ircv3 := n.hasAccountReg()
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
		switch {
		case method == "server":
			// Non-standard "REGISTER <account> <password>" (creates the account and logs in).
			send = func() error { return n.conn.Send("REGISTER", nick, password) }
		case ircv3 && method != "nickserv":
			if email == "" {
				email = "*" // no email (only if the network doesn't require one)
			} else if bad(email) {
				return nil, false, fmt.Errorf("invalid email")
			}
			send = func() error { return n.conn.Send("REGISTER", "*", email, password) }
		default:
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
	n.nsCapture = true
	n.nsMu.Unlock()
	defer func() {
		n.nsMu.Lock()
		n.nsCapture = false
		n.nsMu.Unlock()
	}()
	if err := send(); err != nil {
		return nil, false, err
	}
	time.Sleep(3 * time.Second)
	replies = n.nickServSince(seq)
	for _, r := range replies {
		// 900 RPL_LOGGEDIN: the server logged us in to the new account.
		if strings.HasPrefix(r, "REGISTER SUCCESS") || strings.HasPrefix(r, "VERIFY SUCCESS") || strings.HasPrefix(r, "900 ") {
			ok = true
		}
	}
	return replies, ok, nil
}
