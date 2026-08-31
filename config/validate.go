package config

import (
	"fmt"
	"strings"
)

// ValidateConfig checks structural invariants that YAML unmarshalling alone can't enforce.
func ValidateConfig(cfg *Config) error {
	if len(cfg.IRC.Networks) == 0 {
		return fmt.Errorf("irc: at least one network must be configured")
	}
	seen := make(map[string]bool, len(cfg.IRC.Networks))
	for _, n := range cfg.IRC.Networks {
		name := strings.TrimSpace(n.Name)
		if name == "" {
			return fmt.Errorf("irc: network with empty name (server %q)", n.Server)
		}
		if strings.Contains(name, ":") {
			return fmt.Errorf("irc: network name %q must not contain ':' (used as RSS channel prefix separator)", name)
		}
		fold := strings.ToLower(name)
		if seen[fold] {
			return fmt.Errorf("irc: duplicate network name %q", name)
		}
		seen[fold] = true
	}
	if err := validateNoCrossNetworkChannelOverlap(cfg.IRC.Networks); err != nil {
		return err
	}
	return nil
}

// validateNoCrossNetworkChannelOverlap rejects the same channel name (case-insensitive)
// appearing in more than one network's Channels list. Per-network state (logs, RSS
// announce toggles, admin presence) is keyed by network+channel, but a shared channel
// name across networks is a common config typo (copy-pasting one network's block) worth
// catching up front rather than producing two networks silently mixing traffic for what
// was meant to be one channel.
func validateNoCrossNetworkChannelOverlap(networks []IRCNetworkConfig) error {
	owner := make(map[string]string) // folded channel name -> owning network name
	for _, n := range networks {
		for _, ch := range n.Channels {
			fold := strings.ToLower(strings.TrimSpace(ch.Name))
			if fold == "" {
				continue
			}
			if prev, ok := owner[fold]; ok && prev != n.Name {
				return fmt.Errorf("irc: channel %q is configured on both network %q and %q", ch.Name, prev, n.Name)
			}
			owner[fold] = n.Name
		}
	}
	return nil
}
