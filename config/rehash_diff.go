package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// CloneConfig returns a deep copy of cfg (YAML round-trip).
func CloneConfig(cfg *Config) (*Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var out Config
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	applyStatsDefaults(&out)
	applyUploadsDefaults(&out)
	applyRSSDefaults(&out)
	applyWebDefaults(&out)
	applyFlightDefaults(&out)
	applyOMDBDefaults(&out)
	return &out, nil
}

// IRCEndpointChanged returns true if connection identity changed (reconnect required for new values to apply).
func IRCEndpointChanged(before, after *Config) bool {
	if before == nil || after == nil {
		return false
	}
	return before.IRC.Server != after.IRC.Server ||
		before.IRC.Port != after.IRC.Port ||
		before.IRC.Nickname != after.IRC.Nickname ||
		before.IRC.UseSSL != after.IRC.UseSSL
}

// RehashDiff returns human-readable lines describing what differs between two configs
// (intended for admin NOTICE after a reload). before/after are normalized with the same
// default rules as LoadConfig.
func RehashDiff(before, after *Config) []string {
	if before == nil || after == nil {
		return nil
	}
	b, err := CloneConfig(before)
	if err != nil {
		return []string{fmt.Sprintf("rehash diff: could not clone before: %v", err)}
	}
	a, err := CloneConfig(after)
	if err != nil {
		return []string{fmt.Sprintf("rehash diff: could not clone after: %v", err)}
	}
	var lines []string

	if IRCEndpointChanged(b, a) {
		lines = append(lines, "IRC: server/port/nick/TLS changed (reconnect required; not hot-applied)")
	}

	bNames := append([]string(nil), IRChannelNamesAutoJoin(b.IRC.Channels)...)
	aNames := append([]string(nil), IRChannelNamesAutoJoin(a.IRC.Channels)...)
	sort.Strings(bNames)
	sort.Strings(aNames)
	if added, removed := stringSliceDiff(bNames, aNames); len(added) > 0 || len(removed) > 0 {
		if len(added) > 0 {
			lines = append(lines, "IRC autoin: added "+strings.Join(added, ", "))
		}
		if len(removed) > 0 {
			lines = append(lines, "IRC autoin: removed "+strings.Join(removed, ", "))
		}
	}

	if b.Bot.CommandPrefix != a.Bot.CommandPrefix {
		lines = append(lines, fmt.Sprintf("Bot: command_prefix %q -> %q", b.Bot.CommandPrefix, a.Bot.CommandPrefix))
	}
	if b.Bot.CommandName != a.Bot.CommandName {
		lines = append(lines, fmt.Sprintf("Bot: command_name %q -> %q", b.Bot.CommandName, a.Bot.CommandName))
	}
	rlB := b.Bot.RateLimiting
	rlA := a.Bot.RateLimiting
	if !rateLimitingEqual(rlB, rlA) {
		lines = append(lines, fmt.Sprintf("Bot: rate_limiting %s -> %s", formatRateLimit(rlB), formatRateLimit(rlA)))
	}

	if a.AI.LMStudioURL != b.AI.LMStudioURL || a.AI.Model != b.AI.Model {
		lines = append(lines, "AI: "+aiChangeSummary(b.AI, a.AI))
	}

	if b.RSS.Enabled != a.RSS.Enabled {
		lines = append(lines, fmt.Sprintf("RSS: enabled %v -> %v", b.RSS.Enabled, a.RSS.Enabled))
	}
	if b.RSS.IntervalMinutes != a.RSS.IntervalMinutes {
		lines = append(lines, fmt.Sprintf("RSS: interval_minutes %d -> %d", b.RSS.IntervalMinutes, a.RSS.IntervalMinutes))
	}
	if b.RSS.AnnounceToIRCEnabled() != a.RSS.AnnounceToIRCEnabled() {
		lines = append(lines, fmt.Sprintf("RSS: announce_to_irc %v -> %v", b.RSS.AnnounceToIRCEnabled(), a.RSS.AnnounceToIRCEnabled()))
	}
	if b.RSS.RetentionCount != a.RSS.RetentionCount {
		lines = append(lines, fmt.Sprintf("RSS: retention_count %d -> %d", b.RSS.RetentionCount, a.RSS.RetentionCount))
	}
	if !stringSliceSetEqual(b.RSS.Channels, a.RSS.Channels) {
		lines = append(lines, fmt.Sprintf("RSS: channels list changed (now %d entries)", len(a.RSS.Channels)))
	}
	if !stringSliceSetEqual(b.RSS.FeedURLs, a.RSS.FeedURLs) {
		lines = append(lines, fmt.Sprintf("RSS: feed_urls changed (now %d feeds)", len(a.RSS.FeedURLs)))
	}

	if b.Stats.Enabled != a.Stats.Enabled {
		lines = append(lines, fmt.Sprintf("Stats: enabled %v -> %v", b.Stats.Enabled, a.Stats.Enabled))
	}
	if b.Stats.Interval != a.Stats.Interval {
		lines = append(lines, fmt.Sprintf("Stats: interval (sec) %d -> %d", b.Stats.Interval, a.Stats.Interval))
	}
	if b.Stats.ShouldSaveToDB() != a.Stats.ShouldSaveToDB() {
		lines = append(lines, fmt.Sprintf("Stats: save_to_db %v -> %v", b.Stats.ShouldSaveToDB(), a.Stats.ShouldSaveToDB()))
	}
	if b.Stats.RetentionDays != a.Stats.RetentionDays {
		lines = append(lines, fmt.Sprintf("Stats: retention_days %d -> %d", b.Stats.RetentionDays, a.Stats.RetentionDays))
	}

	if b.Web.Enabled != a.Web.Enabled {
		lines = append(lines, fmt.Sprintf("Web: enabled %v -> %v", b.Web.Enabled, a.Web.Enabled))
	}
	if b.Web.Host != a.Web.Host || b.Web.Port != a.Web.Port {
		lines = append(lines, fmt.Sprintf("Web: listen %s:%d -> %s:%d", b.Web.Host, b.Web.Port, a.Web.Host, a.Web.Port))
	}
	if b.Web.BaseURL != a.Web.BaseURL {
		lines = append(lines, "Web: base_url changed")
	}
	if b.Web.ServerLocation != a.Web.ServerLocation || b.Web.WeatherRefreshMinutes != a.Web.WeatherRefreshMinutes {
		lines = append(lines, "Web: weather settings changed")
	}

	if b.Logger.RotationDays != a.Logger.RotationDays {
		lines = append(lines, fmt.Sprintf("Logger: rotation_days %d -> %d", b.Logger.RotationDays, a.Logger.RotationDays))
	}

	if len(lines) == 0 {
		lines = append(lines, "No substantive config diffs (defaults/normalization only, or identical).")
	}
	return lines
}

func rateLimitingEqual(a, b *RateLimitConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Enabled == b.Enabled && a.Limit == b.Limit && a.Burst == b.Burst && a.Window == b.Window
}

func formatRateLimit(rl *RateLimitConfig) string {
	if rl == nil {
		return "nil"
	}
	return fmt.Sprintf("enabled=%v limit=%d burst=%d window=%d", rl.Enabled, rl.Limit, rl.Burst, rl.Window)
}

func aiChangeSummary(b, a AIConfig) string {
	hb, ha := hostFromURL(b.LMStudioURL), hostFromURL(a.LMStudioURL)
	if b.Model != a.Model && hb != ha {
		return fmt.Sprintf("model %q->%q, host %q->%q", b.Model, a.Model, hb, ha)
	}
	if b.Model != a.Model {
		return fmt.Sprintf("model %q -> %q", b.Model, a.Model)
	}
	return fmt.Sprintf("LM URL host %q -> %q", hb, ha)
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// e.g. missing scheme
		if u, err = url.Parse("http://" + raw); err == nil {
			return u.Host
		}
		return raw
	}
	return u.Host
}

// stringSliceDiff returns added and removed (sorted) comparing a vs b as sets, case-sensitively.
func stringSliceDiff(a, b []string) (added, removed []string) {
	ma := make(map[string]struct{}, len(a))
	for _, s := range a {
		ma[s] = struct{}{}
	}
	mb := make(map[string]struct{}, len(b))
	for _, s := range b {
		mb[s] = struct{}{}
	}
	for s := range mb {
		if _, ok := ma[s]; !ok {
			added = append(added, s)
		}
	}
	for s := range ma {
		if _, ok := mb[s]; !ok {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func stringSliceSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ca := append([]string(nil), a...)
	cb := append([]string(nil), b...)
	sort.Strings(ca)
	sort.Strings(cb)
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}
