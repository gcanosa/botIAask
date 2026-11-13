package config

import (
	"fmt"
	"regexp"
	"strings"
)

// RedditDefaultInterval / RedditMinInterval bound reddit.interval_minutes.
const (
	RedditDefaultInterval = 30
	RedditMinInterval     = 2
)

// RedditConfig: subreddits whose new posts are announced to IRC (public .rss, no API key).
type RedditConfig struct {
	Enabled         bool        `yaml:"enabled"`
	IntervalMinutes int         `yaml:"interval_minutes"`
	Subreddits      []RedditSub `yaml:"subreddits,omitempty"`
}

// RedditSub is one followed subreddit and where its posts go.
type RedditSub struct {
	Name     string   `yaml:"name" json:"name"`                   // bare name, no "r/"
	Channels []string `yaml:"channels,omitempty" json:"channels"` // "network:#chan", see SplitNetworkChannel
	NSFW     bool     `yaml:"nsfw,omitempty" json:"nsfw"`         // allow an NSFW (over18) subreddit
}

var redditNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)

// NormalizeRedditName strips a leading "r/" or "/r/" and spaces; ok is false for anything
// that isn't a bare subreddit name (a pasted URL, "golang+rust", ...).
func NormalizeRedditName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "/")
	if len(s) > 2 && strings.EqualFold(s[:2], "r/") {
		s = s[2:]
	}
	return s, redditNameRe.MatchString(s)
}

// FindRedditSub returns the index of name in subs (case-insensitive), or -1.
func FindRedditSub(subs []RedditSub, name string) int {
	for i, s := range subs {
		if strings.EqualFold(s.Name, name) {
			return i
		}
	}
	return -1
}

func applyRedditDefaults(cfg *Config) {
	if cfg.Reddit.IntervalMinutes <= 0 {
		cfg.Reddit.IntervalMinutes = RedditDefaultInterval
	}
}

// validateReddit: bare unique names, interval floor, channels on configured networks.
func validateReddit(cfg *Config) error {
	r := cfg.Reddit
	if r.IntervalMinutes != 0 && r.IntervalMinutes < RedditMinInterval {
		return fmt.Errorf("reddit: interval_minutes must be at least %d", RedditMinInterval)
	}
	defaultNet := DefaultRSSNetwork(cfg.IRC.Networks)
	seen := map[string]bool{}
	for _, s := range r.Subreddits {
		if !redditNameRe.MatchString(s.Name) {
			return fmt.Errorf("reddit: invalid subreddit name %q (use the bare name, e.g. golang)", s.Name)
		}
		fold := strings.ToLower(s.Name)
		if seen[fold] {
			return fmt.Errorf("reddit: duplicate subreddit %q", s.Name)
		}
		seen[fold] = true
		for _, raw := range s.Channels {
			network, ch := SplitNetworkChannel(strings.TrimSpace(raw), defaultNet)
			if IRCNetworkIndex(cfg.IRC.Networks, network) < 0 {
				return fmt.Errorf("reddit: r/%s: unknown network %q in %q", s.Name, network, raw)
			}
			if _, err := NormalizeYouTubePreview([]string{ch}); err != nil || ch == "*" {
				return fmt.Errorf("reddit: r/%s: invalid channel %q", s.Name, raw)
			}
		}
	}
	return nil
}
