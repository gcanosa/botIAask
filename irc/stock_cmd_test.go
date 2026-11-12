package irc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"botIAask/internal/ttlcache"
)

func TestParseStockArgs(t *testing.T) {
	for _, c := range []struct{ in, q, period string }{
		{"nvidia", "nvidia", "now"},
		{"nvidia 1m", "nvidia", "1m"},
		{"coca cola 5d", "coca cola", "5d"},
		{"1y apple", "apple", "1y"},
		{"1y", "1y", "now"}, // a lone word is the query, never the period
		{"Apple TODAY", "Apple", "1d"},
		{"sp 500", "sp 500", "now"},
		{"btc-usd 3MO", "btc-usd", "3m"},
	} {
		q, p := parseStockArgs(c.in)
		if q != c.q || p.label != c.period {
			t.Errorf("%q: got (%q, %s) want (%q, %s)", c.in, q, p.label, c.q, c.period)
		}
	}
}

func TestSparkline(t *testing.T) {
	if got := sparkline([]float64{1, 2, 3}, 16); got != "▁▅█" {
		t.Errorf("ascending: %q", got)
	}
	if got := sparkline([]float64{5, 5}, 16); got != "▅▅" {
		t.Errorf("flat: %q", got)
	}
	long := make([]float64, 100)
	for i := range long {
		long[i] = float64(i)
	}
	got := sparkline(long, 16)
	if utf8.RuneCountInString(got) != 16 || !strings.HasPrefix(got, "▁") || !strings.HasSuffix(got, "█") {
		t.Errorf("thinned: %q", got)
	}
}

func TestStockChartSkipsHoles(t *testing.T) {
	var r yahooChartResp
	json.Unmarshal([]byte(`{"chart":{"result":[{"meta":{"symbol":"X","regularMarketPrice":3},
		"indicators":{"quote":[{"close":[1,null,3],"high":[1.5,null,null],"low":[0.5,null,2.5]}]}}]}}`), &r)
	c, err := parseStockChart(&r)
	if err != nil || len(c.Bars) != 2 {
		t.Fatalf("bars: %v %v", c, err)
	}
	if b := c.Bars[1]; b.c != 3 || b.h != 3 || b.l != 2.5 { // missing high falls back to the close
		t.Errorf("bar: %+v", b)
	}
}

// fakeYahoo serves one canned instrument ACME (search + chart); "ZZZ" is unknown, "down" is a 500.
func fakeYahoo(t *testing.T, hits *atomic.Int32) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case strings.Contains(r.URL.RawQuery, "down") || strings.HasSuffix(r.URL.Path, "/DOWN"):
			http.Error(w, "boom", 500)
		case strings.HasPrefix(r.URL.Path, "/search"):
			if q := r.URL.Query().Get("q"); strings.Contains(q, "nothing") || q == "zzz" {
				w.Write([]byte(`{"quotes":[]}`))
				return
			}
			w.Write([]byte(`{"quotes":[{"symbol":"ACME","quoteType":"EQUITY"},{"symbol":"ACME=F","quoteType":"FUTURE"},
				{"symbol":"ACMX","quoteType":"ETF"},{"symbol":"ACMEOPT","quoteType":"OPTION"}]}`))
		case strings.HasSuffix(r.URL.Path, "/ACME"):
			w.Write([]byte(`{"chart":{"result":[{"meta":{"symbol":"ACME","currency":"USD","fullExchangeName":"NasdaqGS",
				"longName":"Acme Corp","priceHint":2,"regularMarketPrice":110,"chartPreviousClose":98,
				"regularMarketDayHigh":111,"regularMarketDayLow":108,"fiftyTwoWeekHigh":150,"fiftyTwoWeekLow":80,
				"currentTradingPeriod":{"regular":{"start":1000,"end":2000}}},
				"indicators":{"quote":[{"close":[100,102,101,105,110],"high":[101,103,102,106,111],"low":[99,101,100,104,108]}]}}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldC, oldS := stockChartAPI, stockSearchAPI
	stockChartAPI, stockSearchAPI = srv.URL+"/chart/", srv.URL+"/search?q="
	t.Cleanup(func() { stockChartAPI, stockSearchAPI = oldC, oldS })
	stockChartCache = ttlcache.Cache[stockKey, *stockChart]{TTL: time.Minute, Max: 256}
	stockSearchCache = ttlcache.Cache[string, []stockCand]{TTL: time.Hour, Max: 512}
}

func TestStockCommand(t *testing.T) {
	var hits atomic.Int32
	fakeYahoo(t, &hits)
	n, out := newCmdNetwork(t)
	last := func(rest string) string {
		n.handleStockCommand("#c", "alice", rest)
		l := out.all()
		return l[len(l)-1]
	}
	has := func(got string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("missing %q in %q", w, got)
			}
		}
	}

	// quote: green up arrow, ranges, closed market (the canned session is long over), "also" skips futures/options
	has(last("acme corp"), "\x0303,01[STOCK]\x03 Acme Corp (ACME · NasdaqGS) \x02110.00\x02 USD",
		"\x0309,01▲ +12.00 (+12.24%)\x03 today", "day 108.00–111.00", "52w 80.00–150.00", "market closed", "also: ACMX\x03")
	if got := last("acme corp"); strings.Contains(got, "ACME=F") || strings.Contains(got, "OPT") {
		t.Errorf("also lists noise: %q", got)
	}

	// 5d: whole range against the close before it
	has(last("acme 5d"), "\x025d:\x02 98.00 → \x02110.00\x02 USD", "▲ +12.00 (+12.24%)", "avg 103.60", "low 99.00 · high 111.00", "▁")
	// 3d: last three bars against the close before them
	has(last("acme 3d"), "\x023d:\x02 102.00 → \x02110.00\x02", "▲ +8.00 (+7.84%)", "avg 105.33", "low 100.00 · high 111.00")

	// the second "acme corp" and the 5d/3d pair reuse cached search and chart data
	before := hits.Load()
	last("acme corp")
	last("acme 3d")
	if hits.Load() != before {
		t.Errorf("repeat lookups made %d upstream requests", hits.Load()-before)
	}

	has(last("zzz"), "nothing found for \"zzz\"")  // search hit nothing, literal ticker 404s
	has(last("nothing at all"), "nothing found")   // not even ticker-shaped
	has(last("down"), "Yahoo Finance unreachable") // search 500 and literal 500
	has(last(""), "Usage: !stock <name|ticker>")
}
