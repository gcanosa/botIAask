package rss

import (
	"testing"
	"time"
)

func TestIRCSourceTagMIRC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key  string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"hacker-news", ircSourceHN},
		{"arstechnica", ircSourceFallback + "[ARSTECHNICA]\x03"},
		{"apple", ircSourceFallback + "[APPLE]\x03"},
		{"slashdot", ircSourceFallback + "[SLASHDOT]\x03"},
		{"lobsters-mirror", ircSourceFallback + "[LOBSTERS-MIRROR]\x03"},
		{"a", ircSourceFallback + "[A]\x03"},
		{"---", ircSourceFallback + "[?]\x03"},
	}
	for _, tc := range tests {
		if got := IRCSourceTagMIRC(tc.key); got != tc.want {
			t.Errorf("IRCSourceTagMIRC(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestFormatIRCNewsLine(t *testing.T) {
	t.Parallel()
	at := time.Date(2020, 1, 2, 15, 4, 0, 0, time.UTC)
	e := NewsEntry{
		Title:   "Hello",
		PubDate: at,
		Source:  "hacker-news",
	}
	got := FormatIRCNewsLine(e, "https://x.test/u")
	if want := ircNewsRedOnBlack + ircSourceHN + " 15:04 - Hello" + " \x0312\x1f🔗\x1f\x03 https://x.test/u"; got != want {
		t.Errorf("FormatIRCNewsLine with link mismatch\ngot:  %q\nwant: %q", got, want)
	}
	if got := FormatIRCNewsLine(NewsEntry{Title: "T", PubDate: at, Source: ""}, ""); got != ircNewsRedOnBlack+" 15:04 - T" {
		t.Errorf("empty source: %q", got)
	}
	e.Source = "arstechnica"
	if got := FormatIRCNewsLine(e, ""); got != ircNewsRedOnBlack+ircSourceFallback+"[ARSTECHNICA]\x03 15:04 - Hello" {
		t.Errorf("arstechnica line: %q", got)
	}
}
