package irc

import (
	"testing"

	"botIAask/config"
)

func TestBotFlagChange(t *testing.T) {
	for _, c := range []struct {
		in     string
		on, ok bool
	}{{"+B", true, true}, {"-B", false, true}, {"+iwB", true, true}, {"-B+w", false, true}, {"+iw", false, false}} {
		if on, ok := botFlagChange(c.in); on != c.on || ok != c.ok {
			t.Errorf("%q: got %v,%v want %v,%v", c.in, on, ok, c.on, c.ok)
		}
	}
}

// A 501 for +B turns bot_mode off for that network only, on disk, and rehashes.
func TestDisableBotMode(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := &config.Config{}
	cfg.IRC.Networks = []config.IRCNetworkConfig{
		{Name: "Libera", Server: "irc.libera.chat", Port: 6697, Nickname: "bot", BotMode: true},
		{Name: "other", Server: "irc.example.org", Port: 6697, Nickname: "bot", BotMode: true},
	}
	n := newTestNetwork(cfg)
	n.name = "libera"
	n.SetConfigPath("config.yaml")
	var rehashed string
	n.SetRehashHook(func(src string) error { rehashed = src; return nil })

	n.disableBotMode()

	got, err := config.LoadConfig("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got.IRC.Networks[0].BotMode || !got.IRC.Networks[1].BotMode {
		t.Fatalf("want bot_mode off for Libera only, got %+v", got.IRC.Networks)
	}
	if rehashed == "" {
		t.Fatal("no rehash after disabling bot_mode")
	}
}
