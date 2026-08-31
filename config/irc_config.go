package config

import (
	"fmt"
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
	Name        string         `yaml:"name"`
	Server      string         `yaml:"server"`
	Port        int            `yaml:"port"`
	UseSSL      bool           `yaml:"use_ssl"`
	// TLSSkipVerify disables TLS certificate verification (self-signed certs, bare-IP
	// servers without matching SANs). Only takes effect when UseSSL is true.
	TLSSkipVerify bool         `yaml:"tls_skip_verify,omitempty"`
	Nickname    string         `yaml:"nickname"`
	Channels    []IRChannel    `yaml:"channels"`
	// QuitMessage: optional QUIT reason. Empty uses default: "<app name> <version> Uptime: <uptime>".
	// If set, expand placeholders: {name}, {version}, {uptime}, {nickname}.
	QuitMessage string         `yaml:"quit_message,omitempty"`
	Services    ServicesConfig `yaml:"services"`
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

// IRCNetworkNames returns network names in order (for list diff, logging, etc.).
func IRCNetworkNames(nets []IRCNetworkConfig) []string {
	out := make([]string, len(nets))
	for i, n := range nets {
		out[i] = n.Name
	}
	return out
}
