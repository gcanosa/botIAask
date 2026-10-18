package config

import (
	"fmt"
	"strings"
)

// Channel access levels: what the bot does when a matching nick!user@host joins.
const (
	AccessOp    = "op"    // MODE +o
	AccessVoice = "voice" // MODE +v
	AccessKick  = "kick"  // MODE +b <mask>, then KICK (auto-kick)
)

// ChannelAccess is one auto-op / auto-voice / auto-kick entry of a network
// (IRCNetworkConfig.Access). Mask is a nick!user@host glob (* and ?), matched case-insensitively.
type ChannelAccess struct {
	Channel string `yaml:"channel" json:"channel"`
	Mask    string `yaml:"mask" json:"mask"`
	Level   string `yaml:"level" json:"level"`
	Reason  string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// NormalizeMask turns a nick, user@host or nick!user@host into a full nick!user@host mask:
// "bob" -> "bob!*@*", "*@1.2.3.4" -> "*!*@1.2.3.4", "bob!x" -> "bob!x@*".
func NormalizeMask(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "!"):
		if !strings.Contains(s, "@") {
			s += "@*"
		}
	case strings.Contains(s, "@"):
		s = "*!" + s
	default:
		s += "!*@*"
	}
	return s
}

// MaskMatch reports whether the glob mask (* = any run, ? = one char) matches source, ignoring case.
func MaskMatch(mask, source string) bool {
	m, s := strings.ToLower(mask), strings.ToLower(source)
	star, back := -1, 0
	for i, j := 0, 0; j < len(s); {
		switch {
		case i < len(m) && (m[i] == '?' || m[i] == s[j]):
			i++
			j++
		case i < len(m) && m[i] == '*':
			star, back = i, j
			i++
		case star >= 0:
			back++
			i, j = star+1, back
		default:
			return false
		}
		if j == len(s) {
			return strings.Trim(m[i:], "*") == ""
		}
	}
	return strings.Trim(m, "*") == ""
}

// ValidateAccess normalizes e in place and rejects bad input (shared by IRC and the dashboard).
func ValidateAccess(e *ChannelAccess) error {
	e.Channel = strings.TrimSpace(e.Channel)
	e.Mask = NormalizeMask(e.Mask)
	e.Level = strings.ToLower(strings.TrimSpace(e.Level))
	e.Reason = strings.TrimSpace(e.Reason)
	if e.Channel == "" || (e.Channel[0] != '#' && e.Channel[0] != '&') || strings.ContainsAny(e.Channel, " ,") {
		return fmt.Errorf("invalid channel %q", e.Channel)
	}
	if e.Mask == "" || strings.ContainsAny(e.Mask, " ,") {
		return fmt.Errorf("invalid mask %q", e.Mask)
	}
	if e.Level == AccessKick && strings.Trim(e.Mask, "*!@?.") == "" {
		return fmt.Errorf("mask %q would kick everyone", e.Mask)
	}
	if e.Level != AccessOp && e.Level != AccessVoice && e.Level != AccessKick {
		return fmt.Errorf("level must be op, voice or kick")
	}
	return nil
}

// AccessIndex returns the index of the entry for channel+mask (case-insensitive), or -1.
// A mask has one level per channel: adding it again replaces the level.
func AccessIndex(list []ChannelAccess, channel, mask string) int {
	for i, e := range list {
		if strings.EqualFold(e.Channel, channel) && strings.EqualFold(e.Mask, mask) {
			return i
		}
	}
	return -1
}
