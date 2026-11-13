package config

import "testing"

func TestValidateReddit(t *testing.T) {
	base := func(subs ...RedditSub) *Config {
		return &Config{IRC: IRCConfig{Networks: []IRCNetworkConfig{{Name: "Libera"}}}, Reddit: RedditConfig{IntervalMinutes: 30, Subreddits: subs}}
	}
	if err := ValidateConfig(base(RedditSub{Name: "golang", Channels: []string{"libera:#go"}})); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, cfg := range map[string]*Config{
		"url":      base(RedditSub{Name: "https://reddit.com/r/golang"}),
		"dup":      base(RedditSub{Name: "golang"}, RedditSub{Name: "GoLang"}),
		"network":  base(RedditSub{Name: "golang", Channels: []string{"oftc:#go"}}),
		"channel":  base(RedditSub{Name: "golang", Channels: []string{"libera:go"}}),
		"interval": {IRC: base().IRC, Reddit: RedditConfig{IntervalMinutes: 1}},
	} {
		if ValidateConfig(cfg) == nil {
			t.Errorf("%s: invalid config accepted", name)
		}
	}
	if n, ok := NormalizeRedditName(" /r/golang "); !ok || n != "golang" {
		t.Errorf("NormalizeRedditName = %q %v", n, ok)
	}
}
