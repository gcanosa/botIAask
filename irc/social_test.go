package irc

import (
	"testing"
	"time"
)

func TestParseReminderDelay(t *testing.T) {
	cases := []struct {
		in      string
		wantDur time.Duration
		wantOK  bool
	}{
		{"0", 0, true},          // legacy on-join
		{"30s", 30 * time.Second, true},
		{"15m", 15 * time.Minute, true},
		{"2h", 2 * time.Hour, true},
		{"1h30m", 90 * time.Minute, true},
		{"3d", 72 * time.Hour, true},
		{"", 0, false},          // no token
		{"later", 0, false},     // plain word => treated as note, not a delay
		{"-5m", 0, false},       // negative rejected
		{"0d", 0, false},        // zero days rejected (use "0")
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
