package rss

import "testing"

func TestDedupeBatch(t *testing.T) {
	in := []NewsEntry{
		{GUID: "a", LinkNormalized: "x.com/1"},
		{GUID: "b", LinkNormalized: "x.com/1"}, // same link, other feed
		{GUID: "a"},                            // same guid
		{GUID: "c", DedupKey: "k"},
		{GUID: "d", DedupKey: "k"},
		{GUID: "e"},
	}
	got := dedupeBatch(in)
	if len(got) != 3 || got[0].GUID != "a" || got[1].GUID != "c" || got[2].GUID != "e" {
		t.Fatalf("got %+v", got)
	}
}
