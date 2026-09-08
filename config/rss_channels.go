package config

import "strings"

// SplitNetworkChannel parses "network:#chan" into (network, "#chan"). A bare "#chan"
// (no ':' before the leading '#'/'&') returns (defaultNetwork, raw) for back-compat
// with configs from before multi-network support.
func SplitNetworkChannel(raw, defaultNetwork string) (network, channel string) {
	if i := strings.Index(raw, ":"); i > 0 && !strings.ContainsAny(raw[:i], "#&") {
		return raw[:i], raw[i+1:]
	}
	return defaultNetwork, raw
}

// JoinNetworkChannel builds the "network:#chan" storage form used in RSSConfig.Channels.
func JoinNetworkChannel(network, channel string) string {
	return network + ":" + channel
}

// DefaultRSSNetwork returns the network a bare (unprefixed) legacy RSS.Channels entry
// belongs to: the first configured network, or "" if none.
func DefaultRSSNetwork(nets []IRCNetworkConfig) string {
	if len(nets) == 0 {
		return ""
	}
	return nets[0].Name
}

// RSSChannelContainsFold reports whether list already has an announce entry for network's
// channel — matching either the canonical "network:#chan" form or, when network is
// defaultNetwork, a legacy bare "#chan" entry predating multi-network support. Without the
// bare-form fallback, a pre-multi-network config.yaml (channels: ['#chan']) reports as
// announce-off on the dashboard even while Bot.Broadcast is actively announcing to it (it
// falls back to the first network for a bare entry — see irc/network.go).
func RSSChannelContainsFold(list []string, network, channel, defaultNetwork string) bool {
	ch := strings.TrimSpace(channel)
	if ch == "" {
		return false
	}
	prefixed := JoinNetworkChannel(network, ch)
	bareMatch := network != "" && network == defaultNetwork
	for _, c := range list {
		c = strings.TrimSpace(c)
		if strings.EqualFold(c, prefixed) {
			return true
		}
		if bareMatch && strings.EqualFold(c, ch) {
			return true
		}
	}
	return false
}

// SetRSSChannelAnnounce adds or removes network's channel in the RSS broadcast list.
// Turning on: appends the canonical "network:#chan" form (skipped if already present, in
// either canonical or legacy-bare form). Turning off: removes both the canonical entry AND,
// when network is defaultNetwork, any legacy bare "#chan" entry — so toggling off a
// pre-multi-network channel actually stops the announcements Bot.Broadcast's bare-entry
// fallback was still sending, instead of leaving the untouched bare entry to keep firing.
func SetRSSChannelAnnounce(list []string, network, channel string, on bool, defaultNetwork string) []string {
	ch := strings.TrimSpace(channel)
	if ch == "" {
		return list
	}
	prefixed := JoinNetworkChannel(network, ch)
	bareMatch := network != "" && network == defaultNetwork
	if on {
		if RSSChannelContainsFold(list, network, ch, defaultNetwork) {
			return list
		}
		return append(append([]string(nil), list...), prefixed)
	}
	var out []string
	for _, c := range list {
		trimmed := strings.TrimSpace(c)
		if strings.EqualFold(trimmed, prefixed) {
			continue
		}
		if bareMatch && strings.EqualFold(trimmed, ch) {
			continue
		}
		out = append(out, c)
	}
	return out
}
