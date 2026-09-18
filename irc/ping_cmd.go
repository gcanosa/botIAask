package irc

import (
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus-community/pro-bing"
)

const (
	pingHostMaxLen  = 253
	pingTimeout     = 2 * time.Second
	pingResolveTime = 2 * time.Second
)

func validPingHost(h string) bool {
	if h == "" || len(h) > pingHostMaxLen {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	// Unbracketed IPv6 or hostnames: allowed runes only.
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == ':', r == '[', r == ']', r == '%':
			continue
		default:
			return false
		}
	}
	return true
}

func isICMPNotPermitted(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) {
		return true
	}
	// e.g. Linux "socket: operation not permitted" raw ICMP
	if errno, ok := err.(syscall.Errno); ok {
		if errno == syscall.EPERM || errno == syscall.EACCES {
			return true
		}
	}
	s := err.Error()
	return strings.Contains(s, "operation not permitted") ||
		strings.Contains(s, "Access is denied")
}

// handlePingCommand runs a single unprivileged "ping" (UDP mode on pro-bing; same as many OS ping -n).
func (b *ircNetwork) handlePingCommand(target, sender, host string) {
	if !validPingHost(host) {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: invalid host", sender)))
		return
	}
	pinger, err := probing.NewPinger(host)
	if err != nil {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s unreachable", sender, host)))
		return
	}
	pinger.Count = 1
	pinger.Interval = 0
	pinger.Timeout = pingTimeout
	pinger.ResolveTimeout = pingResolveTime
	// false = unprivileged UDP echo (default); no CAP_NET_RAW required on most systems.
	pinger.SetPrivileged(false)

	if err := pinger.Run(); err != nil {
		if isICMPNotPermitted(err) {
			b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: ICMP not permitted for this process", sender)))
			return
		}
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s unreachable", sender, host)))
		return
	}
	stats := pinger.Statistics()
	if stats != nil && stats.PacketsRecv > 0 {
		rtt := stats.AvgRtt
		if rtt < 0 {
			rtt = 0
		}
		ms := rtt.Round(time.Millisecond).Milliseconds()
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %d ms", sender, ms)))
		return
	}
	b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: %s unreachable", sender, host)))
}

const ctcpPingTimeout = 10 * time.Second

type pendingPing struct {
	token  string
	target string
	start  time.Time
}

// fmtLag renders a duration as ms below one second, otherwise seconds.
func fmtLag(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}

// startCTCPPing sends a CTCP PING to sender and reports the round-trip when the reply
// NOTICE arrives. One in-flight ping per nick (extra !ping calls are dropped silently),
// which together with the command rate limiter keeps this flood-safe.
func (b *ircNetwork) startCTCPPing(target, sender string) {
	key := strings.ToLower(sender)
	tok := fmt.Sprintf("%d", time.Now().UnixNano())

	b.pingMu.Lock()
	if b.pendingPings == nil {
		b.pendingPings = make(map[string]pendingPing)
	}
	if _, busy := b.pendingPings[key]; busy {
		b.pingMu.Unlock()
		return
	}
	b.pendingPings[key] = pendingPing{token: tok, target: target, start: time.Now()}
	b.pingMu.Unlock()

	b.conn.Privmsg(sender, "\x01PING "+tok+"\x01")

	time.AfterFunc(ctcpPingTimeout, func() {
		b.pingMu.Lock()
		p, ok := b.pendingPings[key]
		if ok && p.token == tok {
			delete(b.pendingPings, key)
		} else {
			ok = false
		}
		b.pingMu.Unlock()
		if ok {
			b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: ping timeout (no CTCP PING reply)", sender)))
		}
	})
}

// handlePingReply consumes a CTCP PING reply NOTICE; returns true if it was one.
func (b *ircNetwork) handlePingReply(sender, message string) bool {
	if !strings.HasPrefix(message, "\x01PING") || !strings.HasSuffix(message, "\x01") {
		return false
	}
	tok := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(message, "\x01PING"), "\x01"))
	key := strings.ToLower(sender)

	b.pingMu.Lock()
	p, ok := b.pendingPings[key]
	if ok && p.token == tok {
		delete(b.pendingPings, key)
	} else {
		ok = false
	}
	b.pingMu.Unlock()

	if ok {
		b.sendPrivmsg(p.target, b.sanitize(fmt.Sprintf("@%s: pong! %s", sender, fmtLag(time.Since(p.start)))))
	}
	return true
}
