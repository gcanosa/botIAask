package config

import (
	"reflect"
	"testing"
)

func TestRSSChannelContainsFold(t *testing.T) {
	list := []string{"  libera:##Yerba  ", "libera:#other"}
	if !RSSChannelContainsFold(list, "libera", "##yerba", "libera") {
		t.Fatal("expected match on canonical prefixed form")
	}
	if RSSChannelContainsFold(list, "libera", "#missing", "libera") {
		t.Fatal("expected no match")
	}
}

// TestRSSChannelContainsFold_LegacyBareForm guards the RSS toggle-invisibility bug: a
// pre-multi-network config.yaml entry like "#chan" (no network prefix) must still read as
// "announcing" on the default network, since Bot.Broadcast's bare-entry fallback is actively
// sending to it (see irc/network.go Broadcast/SplitNetworkChannel).
func TestRSSChannelContainsFold_LegacyBareForm(t *testing.T) {
	list := []string{"#legacy"}
	if !RSSChannelContainsFold(list, "libera", "#legacy", "libera") {
		t.Fatal("expected legacy bare entry to match on the default network")
	}
	// Not the default network: a bare entry is ambiguous as to which network it means, so
	// it must not falsely read as "announcing" for a non-default network's same-named channel.
	if RSSChannelContainsFold(list, "oftc", "#legacy", "libera") {
		t.Fatal("bare entry must not match a non-default network")
	}
}

func TestSetRSSChannelAnnounce(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"libera:#a"}, "libera", "#b", true, "libera")
		want := []string{"libera:#a", "libera:#b"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("add_dup_canonical", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"libera:##Yerba"}, "libera", "##yerba", true, "libera")
		want := []string{"libera:##Yerba"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("add_dup_legacy_bare", func(t *testing.T) {
		// Turning "on" a channel that's already announcing via a legacy bare entry must not
		// add a second (canonical) entry, which would double-announce every item.
		got := SetRSSChannelAnnounce([]string{"##Yerba"}, "libera", "##yerba", true, "libera")
		want := []string{"##Yerba"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("remove_canonical", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"libera:#A", "libera:#b"}, "libera", "#a", false, "libera")
		want := []string{"libera:#b"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	// TestSetRSSChannelAnnounce/remove_legacy_bare guards the other half of the toggle-
	// invisibility bug: turning a legacy bare-entry channel "off" must actually remove it
	// (previously it only ever matched/removed the canonical "network:#chan" form, so a
	// bare "#chan" entry survived every "off" toggle and kept announcing).
	t.Run("remove_legacy_bare", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"#legacy", "libera:#other"}, "libera", "#legacy", false, "libera")
		want := []string{"libera:#other"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("remove_legacy_bare_not_default_network_untouched", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"#legacy"}, "oftc", "#legacy", false, "libera")
		want := []string{"#legacy"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v (bare entry belongs to the default network, not oftc)", got, want)
		}
	})
	t.Run("empty_channel", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"libera:#x"}, "libera", "", true, "libera")
		if !reflect.DeepEqual(got, []string{"libera:#x"}) {
			t.Fatalf("got %v", got)
		}
	})
}
