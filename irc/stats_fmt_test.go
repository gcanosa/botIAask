package irc

import "testing"

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
