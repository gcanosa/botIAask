package irc

import (
	"testing"
	"time"

	"botIAask/bookmarks"
)

func TestParseReminderDelay(t *testing.T) {
	cases := []struct {
		in      string
		wantDur time.Duration
		wantOK  bool
	}{
		{"0", 0, true}, // legacy on-join
		{"30s", 30 * time.Second, true},
		{"15m", 15 * time.Minute, true},
		{"2h", 2 * time.Hour, true},
		{"1h30m", 90 * time.Minute, true},
		{"3d", 72 * time.Hour, true},
		{"", 0, false},      // no token
		{"later", 0, false}, // plain word => treated as note, not a delay
		{"-5m", 0, false},   // negative rejected
		{"0d", 0, false},    // zero days rejected (use "0")
	}
	for _, c := range cases {
		gotDur, gotOK := parseReminderDelay(c.in)
		if gotOK != c.wantOK || (gotOK && gotDur != c.wantDur) {
			t.Errorf("parseReminderDelay(%q) = (%v,%v), want (%v,%v)", c.in, gotDur, gotOK, c.wantDur, c.wantOK)
		}
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second: "45s",
		5 * time.Minute:  "5m",
		3 * time.Hour:    "3h",
		50 * time.Hour:   "2d",
		-time.Second:     "0s",
	}
	for d, want := range cases {
		if got := humanizeDuration(d); got != want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestSeenStoredTextDropsPrivateMessages(t *testing.T) {
	if got := seenStoredText("", "my secret"); got != "" {
		t.Errorf("PM text must not be stored, got %q", got)
	}
	if got := seenStoredText("#chan", "hello"); got != "hello" {
		t.Errorf("channel text should be kept, got %q", got)
	}
}

func TestSeenDetailOnlyRepeatsTextInSameChannel(t *testing.T) {
	s := bookmarks.Seen{Action: "message", Channel: "#Staff", Message: "launch codes"}
	if got := seenDetail(s, "#staff"); got != ", saying: launch codes" {
		t.Errorf("same channel (case-insensitive): got %q", got)
	}
	if got := seenDetail(s, "#public"); got != "" {
		t.Errorf("other channel must not see the text, got %q", got)
	}
	if got := seenDetail(s, "bob"); got != "" {
		t.Errorf("PM asker must not see the text, got %q", got)
	}
	if got := seenDetail(bookmarks.Seen{Action: "join"}, "#x"); got != " (join)" {
		t.Errorf("non-message action: got %q", got)
	}
}

func TestSeenTargetsTreatsAmpersandAsChannel(t *testing.T) {
	if ch, reply := seenTargets("&local", "bob"); ch != "&local" || reply != "&local" {
		t.Errorf("&local: got %q,%q", ch, reply)
	}
	if ch, reply := seenTargets("botnick", "bob"); ch != "" || reply != "bob" {
		t.Errorf("PM: got %q,%q", ch, reply)
	}
}
