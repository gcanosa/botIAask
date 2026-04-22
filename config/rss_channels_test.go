package config

import (
	"reflect"
	"testing"
)

func TestRSSChannelContainsFold(t *testing.T) {
	list := []string{"  ##Yerba  ", "#other"}
	if !RSSChannelContainsFold(list, "##yerba") {
		t.Fatal("expected match")
	}
	if RSSChannelContainsFold(list, "#missing") {
		t.Fatal("expected no match")
	}
}

func TestSetRSSChannelAnnounce(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"#a"}, "#b", true, "##B")
		want := []string{"#a", "##B"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("add_dup", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"##Yerba"}, "##yerba", true, "ignored")
		want := []string{"##Yerba"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("add_use_name_when_no_canon", func(t *testing.T) {
		got := SetRSSChannelAnnounce(nil, " #foo ", true, "")
		if len(got) != 1 || got[0] != "#foo" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("remove", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"#A", " #b "}, " #a ", false, "")
		want := []string{" #b "}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
	t.Run("empty_name", func(t *testing.T) {
		got := SetRSSChannelAnnounce([]string{"#x"}, "", true, "y")
		if !reflect.DeepEqual(got, []string{"#x"}) {
			t.Fatalf("got %v", got)
		}
	})
}
