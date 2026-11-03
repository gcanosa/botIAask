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
