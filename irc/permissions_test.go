package irc

import (
	"testing"

	"botIAask/config"
)

func TestIsAdmin_GlobalAppliesToEveryNetwork(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
			{Name: "alpha"},
			{Name: "beta"},
		}},
		Admin: config.AdminConfig{Admins: []string{"global!user@host"}},
	})
	alpha := &ircNetwork{Bot: b, name: "alpha"}
	beta := &ircNetwork{Bot: b, name: "beta"}

	if !alpha.IsAdmin("global!user@host") {
		t.Fatal("global admin.admins entry should be admin on alpha")
	}
	if !beta.IsAdmin("global!user@host") {
		t.Fatal("global admin.admins entry should be admin on beta")
	}
}

func TestIsAdmin_NetworkOnlyDoesNotCrossNetworks(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
			{Name: "alpha", Admins: []string{"alphaadmin!user@host"}},
			{Name: "beta"},
		}},
	})
	alpha := &ircNetwork{Bot: b, name: "alpha"}
	beta := &ircNetwork{Bot: b, name: "beta"}

	if !alpha.IsAdmin("alphaadmin!user@host") {
		t.Fatal("network-only admin should be admin on alpha, its own network")
	}
	if beta.IsAdmin("alphaadmin!user@host") {
		t.Fatal("network-only admin from alpha must not be admin on beta")
	}
}

// TestIgnoreList_ScopedPerNetwork mirrors the !ignore handler's write/read keying
// (irc/bot.go) directly, since sendPrivmsg needs a live *ircevent.Connection this
// test doesn't set up.
func TestIgnoreList_ScopedPerNetwork(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
			{Name: "alpha"},
			{Name: "beta"},
		}},
	})
	alpha := &ircNetwork{Bot: b, name: "alpha"}
	beta := &ircNetwork{Bot: b, name: "beta"}

	b.ignoreList[adminSessionKey(alpha.name, "troll")] = true

	if !b.ignoreList[adminSessionKey(alpha.name, "troll")] {
		t.Fatal("troll should be ignored on alpha")
	}
	if b.ignoreList[adminSessionKey(beta.name, "troll")] {
		t.Fatal("ignoring troll on alpha must not silence them on beta")
	}
}

// TestIgnoreList_PerNetworkIsolation guards the same-shape bug as admin scoping: ignoring a
// nick on one network must not silence the same nick on another, and !unignore must only
// affect the network it was issued on.
func TestIgnoreList_PerNetworkIsolation(t *testing.T) {
	b := newTestBot(&config.Config{
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
			{Name: "alpha"},
			{Name: "beta"},
		}},
	})
	b.ignoreList[adminSessionKey("alpha", "troll")] = true

	if !b.ignoreList[adminSessionKey("alpha", "troll")] {
		t.Fatal("troll should be ignored on alpha")
	}
	if b.ignoreList[adminSessionKey("beta", "troll")] {
		t.Fatal("troll must not be ignored on beta (different network, same nick)")
	}

	// !unignore on beta (a no-op, since troll was never ignored there) must not affect alpha.
	delete(b.ignoreList, adminSessionKey("beta", "troll"))
	if !b.ignoreList[adminSessionKey("alpha", "troll")] {
		t.Fatal("unignore on beta incorrectly cleared alpha's ignore entry")
	}

	// !unignore on alpha clears it there.
	delete(b.ignoreList, adminSessionKey("alpha", "troll"))
	if b.ignoreList[adminSessionKey("alpha", "troll")] {
		t.Fatal("unignore on alpha did not clear the entry")
	}
}
