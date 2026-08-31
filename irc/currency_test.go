package irc

import (
	"testing"
	"time"
)

// TestCachedFetchRatesServesFreshCacheWithoutNetwork verifies the cache-hit path never
// touches the network: a base with a live in-TTL entry returns straight from the map.
func TestCachedFetchRatesServesFreshCacheWithoutNetwork(t *testing.T) {
	const base = "TESTBASE"
	want := &ExchangeRates{Base: base, Rates: map[string]float64{"XYZ": 42}}

	ratesCacheMu.Lock()
	ratesCache[base] = struct {
		rates   *ExchangeRates
		fetched time.Time
	}{rates: want, fetched: time.Now()}
	ratesCacheMu.Unlock()
	t.Cleanup(func() {
		ratesCacheMu.Lock()
		delete(ratesCache, base)
		ratesCacheMu.Unlock()
	})

	got, err := cachedFetchRates(base)
	if err != nil {
		t.Fatalf("cachedFetchRates: unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("cachedFetchRates returned %+v, want the cached entry %+v (should not have re-fetched)", got, want)
	}
}

func TestParseConvertArgs(t *testing.T) {
	cases := []struct {
		in         string
		wantAmount float64
		wantFrom   string
		wantTo     string
		wantErr    bool
	}{
		{"100 usd ars", 100, "USD", "ARS", false},
		{"12.5 EUR usd", 12.5, "EUR", "USD", false},
		{"100 usd", 0, "", "", true},          // too few args
		{"100 usd ars extra", 0, "", "", true}, // too many args
		{"abc usd ars", 0, "", "", true},       // non-numeric amount
		{"", 0, "", "", true},                  // empty
	}
	for _, c := range cases {
		amount, from, to, err := parseConvertArgs(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseConvertArgs(%q): expected error, got none", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseConvertArgs(%q): unexpected error: %v", c.in, err)
			continue
		}
		if amount != c.wantAmount || from != c.wantFrom || to != c.wantTo {
			t.Errorf("parseConvertArgs(%q) = (%v,%v,%v), want (%v,%v,%v)",
				c.in, amount, from, to, c.wantAmount, c.wantFrom, c.wantTo)
		}
	}
}

func TestFormatConvertReply(t *testing.T) {
	got := formatConvertReply("alice", 100, "USD", 92.5, "EUR")
	want := "\x0303,01[CURRENCY]\x03 alice: 100.00 USD = 92.50 EUR"
	if got != want {
		t.Errorf("formatConvertReply() = %q, want %q", got, want)
	}
}

// TestConvertUsesStubbedRates verifies the amount*rate conversion math using a
// stubbed ExchangeRates value (no network).
func TestConvertUsesStubbedRates(t *testing.T) {
	rates := &ExchangeRates{Base: "USD", Rates: map[string]float64{"ARS": 950.5, "EUR": 0.92}}
	amount, from, to, err := parseConvertArgs("10 usd ars")
	if err != nil {
		t.Fatalf("parseConvertArgs: %v", err)
	}
	rate, ok := rates.Rates[to]
	if !ok {
		t.Fatalf("missing rate for %s", to)
	}
	got := formatConvertReply("bob", amount, from, amount*rate, to)
	want := "\x0303,01[CURRENCY]\x03 bob: 10.00 USD = 9505.00 ARS"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
