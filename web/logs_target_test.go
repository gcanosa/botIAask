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
