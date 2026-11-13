package config

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// IRCConfig holds one or more IRC network connections.
type IRCConfig struct {
	Networks []IRCNetworkConfig `yaml:"networks"`
}

// IRCNetworkConfig is one IRC network/server the bot connects to.
type IRCNetworkConfig struct {
	// Name is a short unique label (e.g. "libera") used in logs, admin sessions, and the
	// web dashboard. Defaults to Server when omitted.
	Name string `yaml:"name"`
	// Enabled: nil or true = connect to this network at startup/rehash. False = keep the
	// network block in config (channels, admins, etc. preserved) without connecting to it.
	Enabled *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Server  string `yaml:"server"`
	Port    int    `yaml:"port"`
	UseSSL  bool   `yaml:"use_ssl"`
	// TLSSkipVerify disables TLS certificate verification (self-signed certs, bare-IP
	// servers without matching SANs). Only takes effect when UseSSL is true.
	TLSSkipVerify bool `yaml:"tls_skip_verify,omitempty"`
	// BotMode sets user mode +B (bot flag) on this network after connecting. Off by default;
	// a server that rejects +B (501) gets it switched off automatically (saved to config).
	BotMode  bool        `yaml:"bot_mode,omitempty"`
	Nickname string      `yaml:"nickname"`
	Channels []IRChannel `yaml:"channels"`
	// QuitMessage: optional QUIT reason. Empty uses default: "<app name> <version> Uptime: <uptime>".
	// If set, expand placeholders: {name}, {version}, {uptime}, {nickname}.
	QuitMessage string         `yaml:"quit_message,omitempty"`
	Services    ServicesConfig `yaml:"services"`
	// Admins are extra hostmask fragments granted admin only on this network, on top of
	// the global admin.admins list (which applies to every network).
	Admins []string `yaml:"admins,omitempty"`
	// Access is the auto-op / auto-voice / auto-kick list, applied when a matching user joins.
	Access []ChannelAccess `yaml:"access,omitempty"`
	// YouTubePreview lists the channels where pasted YouTube links get a title/views/likes line
	// ("*" = every channel on this network). Empty = off.
	YouTubePreview []string `yaml:"youtube_preview,omitempty"`
	// Guard lists the channels under the flood guard: +l kept at members+joins (re-set every
	// 30s), plus +m when Moderate. Edited by !guard and the dashboard "Channel flood guard" card.
	Guard []ChannelGuard `yaml:"guard,omitempty"`
	// GuardSettings tunes how the guard moves +l (grace after a (re)join, floor, lowering).
	GuardSettings GuardSettings `yaml:"guard_settings,omitempty"`
}

// GuardSettings: 0 = default for every field; a negative value turns that knob off (0).
type GuardSettings struct {
	// GraceSeconds after the bot (re)joins a channel during which +l is removed and never set,
	// so a server restart's reconnect wave isn't refused with 471. Default 180.
	GraceSeconds int `yaml:"grace_seconds,omitempty"`
	// MinLimit is the lowest +l ever set (ChannelGuard.MinLimit overrides it). Default 30.
	MinLimit int `yaml:"min_limit,omitempty"`
	// LowerDelaySeconds the limit must have been higher than needed before it is lowered. Default 300.
	LowerDelaySeconds int `yaml:"lower_delay_seconds,omitempty"`
	// Threshold: +l is lowered only when it is more than this above what's needed, and a raise
	// adds this much headroom, so +l doesn't flap on every join/part. Default 2.
	Threshold int `yaml:"threshold,omitempty"`
}

// Guard tuning defaults (GuardSettings).
const (
	GuardDefaultGrace      = 180
	GuardDefaultMinLimit   = 30
	GuardDefaultLowerDelay = 300
	GuardDefaultThreshold  = 2
)

// Resolved returns s with defaults filled in (0 = default, negative = 0).
func (s GuardSettings) Resolved() GuardSettings {
	pick := func(v, def int) int {
		if v == 0 {
			return def
		}
		return max(v, 0)
	}
	return GuardSettings{
		GraceSeconds:      pick(s.GraceSeconds, GuardDefaultGrace),
		MinLimit:          pick(s.MinLimit, GuardDefaultMinLimit),
		LowerDelaySeconds: pick(s.LowerDelaySeconds, GuardDefaultLowerDelay),
		Threshold:         pick(s.Threshold, GuardDefaultThreshold),
	}
}

// Flood guard join allowance per 30s: default and maximum.
const (
	GuardDefaultJoins = 5
	GuardMaxJoins     = 50
	// Re-check interval in seconds (ChannelGuard.Interval, 0 = GuardDefaultInterval).
	GuardDefaultInterval = 30
	GuardMinInterval     = 5
	GuardMaxInterval     = 600
)

// ChannelGuard is one guarded channel of a network (IRCNetworkConfig.Guard).
type ChannelGuard struct {
	Channel  string `yaml:"channel" json:"channel"`
	Joins    int    `yaml:"joins" json:"joins"`
	Moderate bool   `yaml:"moderate,omitempty" json:"moderate"`
	Interval int    `yaml:"interval,omitempty" json:"interval"` // seconds between +l re-checks, 0 = default
	MinLimit int    `yaml:"min_limit,omitempty" json:"min_limit"` // lowest +l for this channel, 0 = guard_settings.min_limit
}

// NormalizeGuard validates a guard list: #/& channel names, joins 1-GuardMaxJoins (0 = default),
// one entry per channel (case-insensitive, the last one wins).
func NormalizeGuard(in []ChannelGuard) ([]ChannelGuard, error) {
	out := []ChannelGuard{}
	for _, g := range in {
		g.Channel = strings.TrimSpace(g.Channel)
		if g.Channel == "*" {
			return nil, fmt.Errorf("invalid channel %q", g.Channel)
		}
		if _, err := NormalizeYouTubePreview([]string{g.Channel}); err != nil {
			return nil, err
		}
		if g.Joins == 0 {
			g.Joins = GuardDefaultJoins
		}
		if g.Joins < 1 || g.Joins > GuardMaxJoins {
			return nil, fmt.Errorf("joins for %s must be 1-%d", g.Channel, GuardMaxJoins)
		}
		if g.Interval != 0 && (g.Interval < GuardMinInterval || g.Interval > GuardMaxInterval) {
			return nil, fmt.Errorf("interval for %s must be %d-%d seconds", g.Channel, GuardMinInterval, GuardMaxInterval)
		}
		if g.MinLimit < 0 {
			return nil, fmt.Errorf("min_limit for %s must be >= 0", g.Channel)
		}
		if g.Interval == GuardDefaultInterval {
			g.Interval = 0
		}
		out = slices.DeleteFunc(out, func(o ChannelGuard) bool { return strings.EqualFold(o.Channel, g.Channel) })
		out = append(out, g)
	}
	return out, nil
}

// NormalizeYouTubePreview validates a youtube_preview list: "*" or #/& channel names (no spaces,
// commas or control chars), de-duplicated case-insensitively.
func NormalizeYouTubePreview(in []string) ([]string, error) {
	out := []string{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c != "*" && (len(c) < 2 || len(c) > 64 || (c[0] != '#' && c[0] != '&') || strings.ContainsAny(c, " ,\x07\r\n\x00")) {
			return nil, fmt.Errorf("invalid channel %q", c)
		}
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, c) }) {
			out = append(out, c)
		}
	}
	return out, nil
}

// YouTubePreviewOn reports whether link previews are enabled for channel on this network.
func (n IRCNetworkConfig) YouTubePreviewOn(channel string) bool {
	for _, c := range n.YouTubePreview {
		if c == "*" || strings.EqualFold(c, channel) {
			return true
		}
	}
	return false
}

// IsEnabled reports whether this network should be connected (default: true if unset).
func (n IRCNetworkConfig) IsEnabled() bool {
	if n.Enabled == nil {
		return true
	}
	return *n.Enabled
}

// UnmarshalYAML accepts either the legacy flat single-network shape (server/port/... directly
// under `irc:`) or the current `networks:` list shape, so existing config.yaml files keep
// working unedited. New configs are always saved back out in the `networks:` shape.
func (c *IRCConfig) UnmarshalYAML(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("irc: expected mapping, got yaml kind %d", n.Kind)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "networks" {
			var m struct {
				Networks []IRCNetworkConfig `yaml:"networks"`
			}
			if err := n.Decode(&m); err != nil {
				return err
			}
			c.Networks = m.Networks
			c.normalizeNetworkNames()
			return nil
		}
	}
	// Legacy flat single-network shape.
	var legacy IRCNetworkConfig
	if err := n.Decode(&legacy); err != nil {
		return err
	}
	c.Networks = []IRCNetworkConfig{legacy}
	c.normalizeNetworkNames()
	return nil
}

func (c *IRCConfig) normalizeNetworkNames() {
	for i := range c.Networks {
		if strings.TrimSpace(c.Networks[i].Name) == "" {
			c.Networks[i].Name = c.Networks[i].Server
		}
	}
}

// FindIRCNetworkByName returns the network config entry by name (case-insensitive) or (zero, false).
func FindIRCNetworkByName(nets []IRCNetworkConfig, name string) (IRCNetworkConfig, bool) {
	for _, n := range nets {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return IRCNetworkConfig{}, false
}

// IRCNetworkIndex returns the index of the network called name (case-insensitive), or -1.
func IRCNetworkIndex(nets []IRCNetworkConfig, name string) int {
	for i, n := range nets {
		if strings.EqualFold(n.Name, name) {
			return i
		}
	}
	return -1
}

// IRCNetworkNames returns network names in order (for list diff, logging, etc.).
func IRCNetworkNames(nets []IRCNetworkConfig) []string {
	out := make([]string, len(nets))
	for i, n := range nets {
		out[i] = n.Name
	}
	return out
}
