package irc

import (
	"path/filepath"
	"strings"
	"testing"

	"botIAask/config"
)

func accessNetwork(t *testing.T, access []config.ChannelAccess) (*ircNetwork, *sink) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Admin.Admins = []string{"~admin@admin.host"}
	cfg.IRC.Networks = []config.IRCNetworkConfig{{Name: "test", Server: "irc.example", Port: 6667, Nickname: "bot", Access: access}}
	return newCaptureNetwork(t, cfg)
}

// JOIN enforcement: kick wins over op/voice, admins are never auto-kicked, op wins over voice.
func TestApplyAccess(t *testing.T) {
	n, out := accessNetwork(t, []config.ChannelAccess{
		{Channel: "#c", Mask: "*!*@evil.host", Level: "kick", Reason: "go away"},
		{Channel: "#c", Mask: "*!*@*.host", Level: "voice"},
		{Channel: "#c", Mask: "friend!*@*", Level: "op"},
		{Channel: "#other", Mask: "*!*@good.host", Level: "op"},
	})
	n.applyAccess("#C", "Troll!~t@evil.host")
	n.applyAccess("#c", "admin!~admin@admin.host") // matches voice only
	n.applyAccess("#c", "friend!u@good.host")      // op + voice -> op
	n.applyAccess("#c", "nobody!u@else.net")
	n.applyAccess("#c", "bot!u@evil.host") // ourselves
	want := []string{
		"MODE #C +b *!*@evil.host", "KICK #C Troll go away",
		"MODE #c +v admin",
		"MODE #c +o friend",
	}
	if got := out.all(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// !akick add <mask> / list / del <number> round-trip through config.yaml + rehash.
func TestAkickCommand(t *testing.T) {
	n, out := accessNetwork(t, nil)
	path := filepath.Join(t.TempDir(), "config.yaml")
	n.configPath = path
	n.rehashHook = func(string) error {
		c, err := config.LoadConfig(path)
		if err == nil {
			n.cfg.Store(c)
		}
		return err
	}
	n.handleAccessCommand("#c", "alice", "akick", strings.Fields("add *@spam.net spamming"))
	n.handleAccessCommand("#c", "alice", "akick", strings.Fields("add #c *!*@* nope"))
	n.handleAccessCommand("#c", "alice", "akick", strings.Fields("list"))
	if got := n.netCfg().Access; len(got) != 1 || got[0].Mask != "*!*@spam.net" || got[0].Reason != "spamming" {
		t.Fatalf("access = %+v", got)
	}
	n.handleAccessCommand("#c", "alice", "akick", strings.Fields("del 1"))
	if got := n.netCfg().Access; len(got) != 0 {
		t.Fatalf("after del: %+v", got)
	}
	got := strings.Join(out.all(), "\n")
	for _, w := range []string{"MODE #c +b *!*@spam.net", "would kick everyone", "1. *!*@spam.net — spamming", "MODE #c -b *!*@spam.net", "Auto-kick removed"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestKickCommand(t *testing.T) {
	n, out := accessNetwork(t, nil)
	n.handleKickCommand("#c", "alice", []string{"bob"})
	n.handleKickCommand("alice", "alice", strings.Fields("#c bob flooding"))
	n.handleKickCommand("alice", "alice", []string{"bob"}) // PM without #chan
	got := out.all()
	want := []string{"KICK #c bob Requested by alice", "KICK #c bob flooding", "PRIVMSG alice Kicked bob from #c.", "PRIVMSG alice Usage: !kick [#chan] <nick> [reason]"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

// !aop / !avoice share !akick's code; re-levelling a kick mask to op lifts its ban, and
// each list only shows its own level.
func TestAopAvoiceCommands(t *testing.T) {
	n, out := accessNetwork(t, nil)
	path := filepath.Join(t.TempDir(), "config.yaml")
	n.configPath = path
	n.rehashHook = func(string) error {
		c, err := config.LoadConfig(path)
		if err == nil {
			n.cfg.Store(c)
		}
		return err
	}
	n.handleAccessCommand("#c", "alice", "akick", strings.Fields("add *!*@x.net"))
	n.handleAccessCommand("#c", "alice", "aop", strings.Fields("add *!*@x.net"))
	n.handleAccessCommand("alice", "alice", "avoice", strings.Fields("add #c *!v@y.net"))
	n.handleAccessCommand("#c", "alice", "akick", strings.Fields("list"))
	n.handleAccessCommand("#c", "alice", "aop", strings.Fields("list"))
	n.handleAccessCommand("#c", "alice", "avoice", strings.Fields("del 1"))
	acc := n.netCfg().Access
	if len(acc) != 1 || acc[0].Level != "op" || acc[0].Mask != "*!*@x.net" {
		t.Fatalf("access = %+v", acc)
	}
	got := strings.Join(out.all(), "\n")
	for _, w := range []string{"MODE #c -b *!*@x.net", "Auto-op added on #c: *!*@x.net", "Auto-voice added on #c: *!v@y.net", "No auto-kick entries on #c.", "1. *!*@x.net", "Auto-voice removed on #c: *!v@y.net."} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}
