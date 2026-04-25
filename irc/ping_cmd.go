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
func (b *Bot) handlePingCommand(target, sender, host string) {
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
