package irc

import (
	"testing"

	"botIAask/logger"
)

func TestStatsFmt(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := statsGroup(in); got != want {
			t.Errorf("statsGroup(%d)=%q want %q", in, got, want)
		}
	}
	for p, want := range map[float64]string{10: statsGreen, 60: statsYellow, 84.9: statsYellow, 85: statsRed} {
		if got := statsPctColor(p); got != want {
			t.Errorf("statsPctColor(%v)=%q want %q", p, got, want)
		}
	}
	if statsNum(-1) != statsColor(statsGray, "n/a") {
		t.Error("negative should render n/a")
	}
}

func TestChanStatsLines(t *testing.T) {
	a := logger.Activity{Total: 30, Days: 2, Nicks: map[string]int{"alice": 20, "bob": 10}}
	a.Hours[14], a.Hours[3], a.Weekday[4] = 20, 10, 30
	ls := chanStatsLines("#c", 7, a)
	if len(ls) != 4 {
		t.Fatalf("got %d lines", len(ls))
	}
	for _, l := range ls {
		if len(l) > ircTextBudget-40 {
			t.Fatalf("line too long (%d): %q", len(l), l)
		}
	}
}
