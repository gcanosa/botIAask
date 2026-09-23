package rss

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"botIAask/config"
)

type recBot struct {
	mu   sync.Mutex
	msgs []string
}

func (b *recBot) Broadcast(_ []string, m string) {
	b.mu.Lock()
	b.msgs = append(b.msgs, m)
	b.mu.Unlock()
}
func (b *recBot) IsConnected() bool { return true }
func (b *recBot) count() int        { b.mu.Lock(); defer b.mu.Unlock(); return len(b.msgs) }

func feedXML(n int) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>http://x/</link>`)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ { // item 0 newest
		sb.WriteString(fmt.Sprintf(`<item><title>item %d</title><link>https://example.com/a/%d</link><guid>g%d</guid><pubDate>%s</pubDate></item>`,
			i, i, i, base.Add(-time.Duration(i)*time.Hour).Format(time.RFC1123Z)))
	}
	sb.WriteString(`</channel></rss>`)
	return sb.String()
}

func setup(t *testing.T, handler http.HandlerFunc, retention int) (*Fetcher, *recBot, *Database) {
	t.Helper()
	announceDelay = 0
	shortenURL = func(u, _ string) string { return u }
	t.Cleanup(func() { announceDelay = 3 * time.Second; shortenURL = ShortenURLWithService })
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	db, err := NewDatabase(t.TempDir() + "/rss.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bot := &recBot{}
	cfg := &config.Config{RSS: config.RSSConfig{Enabled: true, IntervalMinutes: 30, RetentionCount: retention,
		Channels: []string{"#c"}, FeedURLs: []string{srv.URL}}}
	return NewFetcher(cfg, bot, db), bot, db
}

func xmlHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, body)
	}
}

// R1/R4: a new 60-item feed announces only the seed batch, and a second cycle with
// retention < feed length announces nothing (no pruned-then-repeated items).
func TestNewFeedSeedsAndDoesNotRepeat(t *testing.T) {
	f, bot, _ := setup(t, xmlHandler(feedXML(60)), 50)
	f.Fetch()
	if got := bot.count(); got != seedAnnounce {
		t.Fatalf("first cycle announced %d, want %d", got, seedAnnounce)
	}
	f.Fetch()
	f.Fetch()
	if got := bot.count(); got != seedAnnounce {
		t.Fatalf("later cycles re-announced: total %d, want %d", got, seedAnnounce)
	}
}

// Per-cycle cap: known feed gets 15 new items -> 10 now, 5 next cycle, none repeated.
func TestPerCycleCapDefersLeftovers(t *testing.T) {
	body := feedXML(5)
	f, bot, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { xmlHandler(body)(w, r) }, 50)
	f.Fetch() // 5 items <= seedAnnounce? no: 5 > 3 -> seeds 3
	base := bot.count()
	body = feedXML(20) // items 0..19; the original 0..4 are known
	f.Fetch()
	if got := bot.count() - base; got != maxAnnouncePerCycle {
		t.Fatalf("announced %d, want cap %d", got, maxAnnouncePerCycle)
	}
	f.Fetch()
	if got := bot.count() - base; got != 15 {
		t.Fatalf("after 2nd cycle announced %d, want 15", got)
	}
	f.Fetch()
	if got := bot.count() - base; got != 15 {
		t.Fatalf("repeat announce: %d", got)
	}
}

// R5: bot drops mid-loop -> remaining items stay unseen.
type flakyBot struct {
	recBot
	calls int
}

func (b *flakyBot) IsConnected() bool { b.calls++; return b.calls <= 3 } // start-check + two loop checks

func TestDisconnectDefersItems(t *testing.T) {
	f, _, db := setup(t, xmlHandler(feedXML(3)), 50)
	fb := &flakyBot{}
	f.bot = fb
	f.Fetch()
	n, _ := db.CountSeenNews()
	if n >= 3 || n == 0 {
		t.Fatalf("seen %d rows, want partial (1-2)", n)
	}
}

// R7: conditional GET -> 304 keeps the feed OK with no re-parse; UA is set.
func TestConditionalGet(t *testing.T) {
	var gotUA string
	var notMod int
	f, bot, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		if r.Header.Get("If-None-Match") == `"v1"` {
			notMod++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		xmlHandler(feedXML(2))(w, r)
	}, 50)
	f.Fetch()
	f.Fetch()
	if notMod != 1 {
		t.Fatalf("expected one 304, got %d", notMod)
	}
	if !strings.HasPrefix(gotUA, "botIAask/") {
		t.Fatalf("UA = %q", gotUA)
	}
	if st := f.FeedStatuses(); len(st) != 1 || !st[0].OK {
		t.Fatalf("status after 304: %+v", st)
	}
	if bot.count() != 2 {
		t.Fatalf("announced %d, want 2", bot.count())
	}
}

// R2: pruning keeps recently-seen rows even beyond retention.
func TestCleanupKeepsLiveRows(t *testing.T) {
	_, _, db := setup(t, xmlHandler(feedXML(1)), 50)
	for i := 0; i < 10; i++ {
		e := NewsEntry{GUID: fmt.Sprint("g", i), Title: "t", Source: "s", DedupKey: fmt.Sprint("d", i)}
		if err := db.MarkSeen(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CleanupPerSource(3); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.CountSeenNews(); n != 10 {
		t.Fatalf("live rows pruned: %d left", n)
	}
	db.db.Exec(`UPDATE seen_news SET last_seen = datetime('now','-3 days'), added_at = datetime('now','-3 days')`)
	db.CleanupPerSource(3)
	if n, _ := db.CountSeenNews(); n != 3 {
		t.Fatalf("stale rows kept: %d, want 3", n)
	}
}
