package web

import (
	"testing"

	"botIAask/config"
)

func TestShortLinkBase(t *testing.T) {
	cfg := &config.Config{Web: config.WebConfig{BaseURL: "http://sekurnet.duckdns.org:3366/"}}
	if got := shortLinkBase(cfg); got != "http://sekurnet.duckdns.org:3366" {
		t.Errorf("http: %q", got)
	}
	cfg.ShortLinks.HTTPS = true
	if got := shortLinkBase(cfg); got != "https://sekurnet.duckdns.org:3366" {
		t.Errorf("https: %q", got)
	}
	if got := shortLinkBase(&config.Config{}); got != "" {
		t.Errorf("empty base_url: %q", got)
	}
}
