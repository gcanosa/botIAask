package web

import (
	"strings"
	"testing"
)

func TestParseExpiryDays(t *testing.T) {
	for in, want := range map[string]int{
		"": 7, "abc": 7, "30": 30, "0": 0, "-5": 0, "99999999999": 3650, " 14 ": 14,
	} {
		if got := parseExpiryDays(in); got != want {
			t.Errorf("parseExpiryDays(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNewTicketIDIs48Bits(t *testing.T) {
	a, b := newTicketID(), newTicketID()
	if len(a) != 12 || a == b || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("unexpected ticket ids %q %q", a, b)
	}
}
