package irc

import (
	"path/filepath"
	"strings"
	"testing"

	"botIAask/config"
)

func TestRedditCommand(t *testing.T) {
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
	run := func(target, args string) { n.handleRedditCommand(target, "alice", strings.Fields(args)) }
	run("#c", "add r/golang")            // current channel
	run("alice", "add golang #d --nsfw") // PM, bare channel = this network; merged
	run("alice", "add rust")             // PM without channel: refused
	run("#c", "add bad/name")
	run("#c", "list")
	run("#c", "del golang #c")
	run("#c", "del rust")
	subs := n.getCfg().Reddit.Subreddits
	if len(subs) != 1 || subs[0].Name != "golang" || !subs[0].NSFW || strings.Join(subs[0].Channels, ",") != "test:#d" {
		t.Fatalf("subs = %+v", subs)
	}
	run("#c", "del golang")
	if len(n.getCfg().Reddit.Subreddits) != 0 {
		t.Fatal("not removed")
	}
	got := strings.Join(out.all(), "\n")
	for _, want := range []string{
		"PRIVMSG #c Following r/golang in test:#c.",
		"PRIVMSG alice Following r/golang in test:#d.",
		"PRIVMSG alice Name at least one channel",
		"PRIVMSG #c Invalid subreddit name",
		"PRIVMSG #c - r/golang  channels=test:#c,test:#d nsfw",
		"PRIVMSG #c r/golang no longer announced in test:#c.",
		"PRIVMSG #c r/rust is not followed.",
		"PRIVMSG #c Stopped following r/golang.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
