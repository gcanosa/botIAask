package web

import (
	"testing"

	"botIAask/config"
)

func TestValidLogTarget(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "libera"}}
	cases := []struct {
		ch, net string
		ok      bool
	}{
		{"#go", "", true},
		{"#go", "libera", true},
		{"&local", "libera", true},
		{"nick", "libera", false},     // would map to the private-message log
		{"", "libera", false},         //
		{"#go", "../../etc/x", false}, // traversal via network
		{"#go", "unknown-net", false},
	}
	for _, c := range cases {
		if got := validLogTarget(cfg, c.ch, c.net); got != c.ok {
			t.Errorf("validLogTarget(%q,%q)=%v want %v", c.ch, c.net, got, c.ok)
		}
	}
}

func TestIsNetworkLogKey(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "irc.libera.chat"}}
	if !isNetworkLogKey(cfg, "irc.libera.chat") {
		t.Fatal("network name is the PM log key and must be refused as a channel fallback")
	}
	if isNetworkLogKey(cfg, "go") {
		t.Fatal("ordinary channel key misclassified")
	}
}

func TestIsKeyedChannel(t *testing.T) {
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{
		{Name: "a", Channels: []config.IRChannel{{Name: "#pub"}, {Name: "#staff", Password: "k"}}},
		{Name: "b", Channels: []config.IRChannel{{Name: "#staff"}}},
	}
	if !isKeyedChannel(cfg, "#staff", "a") || !isKeyedChannel(cfg, "#STAFF", "") {
		t.Fatal("keyed channel not detected")
	}
	if isKeyedChannel(cfg, "#staff", "b") || isKeyedChannel(cfg, "#pub", "a") {
		t.Fatal("unkeyed channel misclassified")
	}
}
