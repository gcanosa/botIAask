package rss

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
	"botIAask/meta"
	"github.com/mmcdole/gofeed"
)

type BotInterface interface {
	Broadcast(channels []string, message string)
	IsConnected() bool
}

// FeedStatus is the last fetch result for a configured feed URL (used by the admin API).
type FeedStatus struct {
	URL   string     `json:"url"`
	OK    bool       `json:"ok"`
	Error string     `json:"error,omitempty"`
	Label string     `json:"label"`
	At    *time.Time `json:"at,omitempty"`
}

type lastFeedFetch struct {
	OK    bool
	Err   string
	Label string
	At    time.Time
}

// feedHTTPClient bounds gofeed's fetch of each feed URL so one hung feed can't
// stall the rest of the cycle (gofeed's own default client has no timeout).
var feedHTTPClient = &http.Client{Timeout: 30 * time.Second}

const (
	// seedAnnounce: when a feed has no known items at all (new feed / wiped DB), only its newest
	// N items are announced; the rest are recorded silently so adding a feed can't flood channels.
	seedAnnounce = 3
	// maxAnnouncePerCycle caps IRC output per fetch cycle; leftovers stay unseen and go out next cycle.
	maxAnnouncePerCycle = 10
	// maxFeedBytes bounds a single feed download.
	maxFeedBytes = 10 << 20
	// conditionalMaxAge: force a full download at least this often even if the server keeps
	// answering 304, so last_seen keeps being refreshed for every item still in the feed.
	conditionalMaxAge = 12 * time.Hour
)

// Package-level so tests can run without sleeping or hitting real shorteners.
var (
	announceDelay = 3 * time.Second
	shortenURL    = ShortenURLWithService
)

type feedValidators struct {
	etag, lastModified string
	fullAt             time.Time
}

type Fetcher struct {
	cfg        *config.Config
	bot        BotInterface
	db         *Database
	mu         sync.Mutex
	enabled    bool
	stopChan   chan struct{}
	lastFetch  time.Time
	lfMu       sync.RWMutex
	feedLast   map[string]lastFeedFetch
	feedLastMu sync.RWMutex
	// fetchMu serialises Fetch/Backfill (ticker, "fetch now", config restarts) so one item is never
	// announced twice; validators is only touched while it is held.
	fetchMu    sync.Mutex
	validators map[string]feedValidators
}

func NewFetcher(cfg *config.Config, bot BotInterface, db *Database) *Fetcher {
	return &Fetcher{
		cfg:        cfg,
		bot:        bot,
		db:         db,
		enabled:    cfg.RSS.Enabled,
		stopChan:   make(chan struct{}),
		feedLast:   make(map[string]lastFeedFetch),
		validators: make(map[string]feedValidators),
	}
}

func (f *Fetcher) Start() {
	f.mu.Lock()
	if !f.enabled {
		f.mu.Unlock()
		return
	}
	// Capture the current stop channel once; Stop()/SetEnabled(false) close it and
	// install a fresh one for the next run, so reading f.stopChan again later in this
	// loop could observe the new (open) channel and miss the close entirely.
	stop := f.stopChan
	intervalMin := f.cfg.RSS.IntervalMinutes
	f.mu.Unlock()
	if intervalMin <= 0 {
		log.Printf("[RSS] interval_minutes=%d is invalid, using 30", intervalMin)
		intervalMin = 30
	}

	ticker := time.NewTicker(time.Duration(intervalMin) * time.Minute)
	defer ticker.Stop()

	// Wait for bot to be connected before initial fetch (up to 2 minutes), honoring stop
	// so a Stop() during this wait doesn't leave the loop running past shutdown.
	for i := 0; i < 24; i++ {
		if f.bot.IsConnected() {
			break
		}
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}

	// Initial fetch
	f.Fetch()

	for {
		select {
		case <-ticker.C:
			f.Fetch()
		case <-stop:
			return
		}
	}
}

func (f *Fetcher) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enabled {
		close(f.stopChan)
		f.enabled = false
		f.stopChan = make(chan struct{})
	}
}

func (f *Fetcher) SetEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.enabled == enabled {
		return
	}

	if enabled {
		f.enabled = true
		guard.Go("rss fetcher", f.Start)
	} else {
		close(f.stopChan)
		f.enabled = false
		f.stopChan = make(chan struct{})
	}
}

func (f *Fetcher) IsEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

// SetConfig atomically replaces the live config without restarting the fetch loop.
// Use this when only non-structural settings change (e.g. URL shortener, announce flag).
func (f *Fetcher) SetConfig(cfg *config.Config) {
	f.mu.Lock()
	f.cfg = cfg
	f.mu.Unlock()
}

// ApplyConfig swaps in a new root config and restarts the fetch loop only when RSS.Enabled
// or the ticker interval actually changed, so a rehash that touches unrelated settings
// (e.g. announce flag, URL shortener) doesn't tear down and relaunch the running loop.
func (f *Fetcher) ApplyConfig(cfg *config.Config) {
	f.mu.Lock()
	sameLoop := f.enabled == cfg.RSS.Enabled && f.cfg.RSS.IntervalMinutes == cfg.RSS.IntervalMinutes
	f.cfg = cfg
	f.mu.Unlock()

	if sameLoop {
		return
	}
	if f.IsEnabled() {
		f.Stop()
	}
	if cfg.RSS.Enabled {
		f.SetEnabled(true)
	}
}

func (f *Fetcher) GetLastFetchTime() time.Time {
	f.lfMu.RLock()
	defer f.lfMu.RUnlock()
	return f.lastFetch
}

func (f *Fetcher) GetDB() *Database {
	return f.db
}

// FeedStatuses returns one row per configured feed URL, in order, for the admin UI.
func (f *Fetcher) FeedStatuses() []FeedStatus {
	f.mu.Lock()
	urls := f.cfg.RSS.FeedURLs
	f.mu.Unlock()
	f.feedLastMu.RLock()
	defer f.feedLastMu.RUnlock()
	out := make([]FeedStatus, 0, len(urls))
	for _, u := range urls {
		if u == "" {
			continue
		}
		if st, ok := f.feedLast[u]; ok {
			t := st.At
			out = append(out, FeedStatus{URL: u, OK: st.OK, Error: st.Err, Label: st.Label, At: &t})
			continue
		}
		out = append(out, FeedStatus{
			URL:   u,
			OK:    false,
			Error: "not yet fetched",
			Label: FeedLabelFallback(u),
		})
	}
	return out
}

func feedDisplayLabel(feedURL string, feed *gofeed.Feed) string {
	if feed != nil {
		if t := strings.TrimSpace(feed.Title); t != "" {
			return t
		}
	}
	return FeedLabelFallback(feedURL)
}

// fetchFeed downloads and parses one feed with an identifying User-Agent, conditional GET
// (ETag / Last-Modified) when conditional is set, and a body size cap. notModified is true on 304.
// Callers must hold fetchMu (validators map).
func (f *Fetcher) fetchFeed(fp *gofeed.Parser, feedURL string, conditional bool) (feed *gofeed.Feed, notModified bool, err error) {
	req, err := http.NewRequest(http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (+RSS reader)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.8")
	if v, ok := f.validators[feedURL]; conditional && ok && time.Since(v.fullAt) < conditionalMaxAge {
		if v.etag != "" {
			req.Header.Set("If-None-Match", v.etag)
		}
		if v.lastModified != "" {
			req.Header.Set("If-Modified-Since", v.lastModified)
		}
	}
	resp, err := feedHTTPClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, false, fmt.Errorf("http status %d", resp.StatusCode)
	}
	feed, err = fp.Parse(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return nil, false, err
	}
	f.validators[feedURL] = feedValidators{
		etag:         resp.Header.Get("ETag"),
		lastModified: resp.Header.Get("Last-Modified"),
		fullAt:       time.Now(),
	}
	return feed, false, nil
}

// newEntriesFrom returns the feed items not yet stored (touching last_seen on the known ones).
// A feed with items but none known (new feed / wiped DB) is "seeded": everything except the
// newest seedAnnounce items is recorded silently, and only those are returned for announcing.
func (f *Fetcher) newEntriesFrom(feed *gofeed.Feed, feedURL string) []NewsEntry {
	src := FeedSourceKeyFromFeed(feedURL, feed)
	srcIcon := SourceIconForFeedURL(feed, feedURL)
	var fresh []NewsEntry
	known := 0
	for _, item := range feed.Items {
		entry, ok := EntryFromFeedItem(item, src, srcIcon)
		if !ok {
			continue
		}
		dup, err := f.db.NewsItemDuplicate(entry.GUID, entry.DedupKey, entry.LinkNormalized)
		if err != nil {
			log.Printf("[RSS] DB Error: %v", err)
			continue
		}
		if dup {
			known++
			continue
		}
		fresh = append(fresh, entry)
	}
	if known == 0 && len(fresh) > seedAnnounce {
		sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].PubDate.Before(fresh[j].PubDate) })
		silent := fresh[:len(fresh)-seedAnnounce]
		for _, e := range silent {
			if err := f.db.MarkSeen(e); err != nil {
				log.Printf("[RSS] Failed to seed entry: %v", err)
			}
		}
		log.Printf("[RSS] Feed %s is new: recorded %d items silently, announcing newest %d", feedURL, len(silent), seedAnnounce)
		fresh = fresh[len(fresh)-seedAnnounce:]
	}
	return fresh
}

func (f *Fetcher) Fetch() {
	if !f.bot.IsConnected() {
		return
	}
	if !f.fetchMu.TryLock() {
		return // a cycle is already running
	}
	defer f.fetchMu.Unlock()

	f.mu.Lock()
	cfg := f.cfg
	f.mu.Unlock()

	f.lfMu.Lock()
	f.lastFetch = time.Now()
	f.lfMu.Unlock()

	fp := gofeed.NewParser()
	var newEntries []NewsEntry
	perFeed := make(map[string]lastFeedFetch, len(cfg.RSS.FeedURLs))

	for _, feedURL := range cfg.RSS.FeedURLs {
		if feedURL == "" {
			continue
		}
		feed, notModified, err := f.fetchFeed(fp, feedURL, true)
		at := time.Now()
		if err != nil {
			log.Printf("[RSS] Error fetching feed %s: %v", feedURL, err)
			perFeed[feedURL] = lastFeedFetch{OK: false, Err: err.Error(), Label: FeedLabelFallback(feedURL), At: at}
			continue
		}
		if notModified {
			f.feedLastMu.RLock()
			prev, had := f.feedLast[feedURL]
			f.feedLastMu.RUnlock()
			label := FeedLabelFallback(feedURL)
			if had && prev.Label != "" {
				label = prev.Label
			}
			perFeed[feedURL] = lastFeedFetch{OK: true, Label: label, At: at}
			continue
		}

		perFeed[feedURL] = lastFeedFetch{OK: true, Label: feedDisplayLabel(feedURL, feed), At: at}
		newEntries = append(newEntries, f.newEntriesFrom(feed, feedURL)...)
	}

	f.feedLastMu.Lock()
	f.feedLast = perFeed
	f.feedLastMu.Unlock()

	// Oldest first across all feeds; announced items are limited per cycle (leftovers stay
	// unseen and are picked up next cycle).
	sort.SliceStable(newEntries, func(i, j int) bool { return newEntries[i].PubDate.Before(newEntries[j].PubDate) })
	announce := cfg.RSS.AnnounceToIRCEnabled()
	if announce && len(newEntries) > maxAnnouncePerCycle {
		log.Printf("[RSS] %d new items; announcing %d now, rest next cycle", len(newEntries), maxAnnouncePerCycle)
		newEntries = newEntries[:maxAnnouncePerCycle]
	}

	for _, entry := range newEntries {
		// Don't burn items while IRC is down: they stay unseen and are announced after reconnect.
		if announce && !f.bot.IsConnected() {
			log.Printf("[RSS] IRC disconnected mid-announce; deferring remaining items")
			break
		}
		entry.ShortLink = shortenURL(entry.Link, cfg.RSS.URLShortener)

		if announce {
			f.bot.Broadcast(cfg.RSS.Channels, FormatIRCNewsLine(entry, entry.ShortLink))
		}
		if err := f.db.MarkSeen(entry); err != nil {
			log.Printf("[RSS] Failed to mark seen: %v", err)
			continue
		}
		if announce {
			time.Sleep(announceDelay)
		}
	}

	// Cleanup old entries
	retention := cfg.RSS.RetentionCount
	if retention <= 0 {
		retention = 50 // Default fallback
	}
	if err := f.db.CleanupPerSource(retention); err != nil {
		log.Printf("[RSS] Cleanup Error: %v", err)
	}
}

// Backfill populates the database with the latest X items without broadcasting them.
func (f *Fetcher) Backfill(limit int) int {
	f.fetchMu.Lock()
	defer f.fetchMu.Unlock()

	f.mu.Lock()
	cfg := f.cfg
	f.mu.Unlock()

	fp := gofeed.NewParser()
	totalAdded := 0

	for _, feedURL := range cfg.RSS.FeedURLs {
		feed, _, err := f.fetchFeed(fp, feedURL, false)
		if err != nil {
			log.Printf("[RSS] Error fetching feed %s for backfill: %v", feedURL, err)
			continue
		}
		src := FeedSourceKeyFromFeed(feedURL, feed)
		srcIcon := SourceIconForFeedURL(feed, feedURL)

		log.Printf("[RSS] Feed %s fetched: %d items total", feedURL, len(feed.Items))

		addedThisFeed := 0
		for _, item := range feed.Items {
			if addedThisFeed >= limit {
				break
			}
			entry, ok := EntryFromFeedItem(item, src, srcIcon)
			if !ok {
				continue
			}
			dup, err := f.db.NewsItemDuplicate(entry.GUID, entry.DedupKey, entry.LinkNormalized)
			if err != nil {
				log.Printf("[RSS] DB Error during backfill: %v", err)
				continue
			}
			if dup {
				continue
			}
			entry.ShortLink = shortenURL(entry.Link, cfg.RSS.URLShortener)
			if err := f.db.MarkSeen(entry); err != nil {
				log.Printf("[RSS] Failed to save backfill entry: %v", err)
				continue
			}
			addedThisFeed++
			totalAdded++
		}
	}
	return totalAdded
}

// shortenerServices lists the shorteners offered in the web UI. Each one
// must respond to a plain unauthenticated GET with a bare short URL in the
// response body (no JS challenge, no auth) since doShorten does nothing more
// than that. Verified working 2026-08-31; dropped from an earlier list of 8:
// shorturl.at (Cloudflare bot-challenge blocks scripted GETs), turl.at and
// po.st (domains dead/discontinued), bc.vc (302 redirect instead of a plain
// short URL, and historically known for injecting ads into destination pages).
// da.gd added afterward: same operator/API shape as is.gd/v.gd, verified working.
var shortenerServices = []struct {
	name string
	fn   func(string) (string, error)
}{
	{"is.gd", shortenWithIsGd},
	{"tinyurl", shortenWithTinyURL},
	{"v.gd", shortenWithVGd},
	{"clck.ru", shortenWithClckRu},
	{"da.gd", shortenWithDaGd},
}

// parseShortURL reads a plain-text shortener response and validates it looks like a URL.
func parseShortURL(body []byte) (string, error) {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "", fmt.Errorf("empty response")
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "", fmt.Errorf("response is not a URL: %.80s", s)
	}
	return s, nil
}

// shortenerClient bounds the URL-shortener GET so a hung service can't stall
// the announce path. ponytail: shared client, no per-call ctx needed here.
var shortenerClient = &http.Client{Timeout: 10 * time.Second}

// doShorten performs a GET to apiURL and returns the plain-text short URL.
func doShorten(apiURL string) (string, error) {
	resp, err := shortenerClient.Get(apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return parseShortURL(body)
}

func shortenWithIsGd(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://is.gd/create.php?format=simple&url=%s", url.QueryEscape(longURL)))
}

func shortenWithTinyURL(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://tinyurl.com/api-create.php?url=%s", url.QueryEscape(longURL)))
}

func shortenWithVGd(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://v.gd/create.php?format=simple&url=%s", url.QueryEscape(longURL)))
}

func shortenWithClckRu(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://clck.ru/--?url=%s", url.QueryEscape(longURL)))
}

func shortenWithDaGd(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://da.gd/shorten?url=%s", url.QueryEscape(longURL)))
}

// ShortenURL shortens a URL using the configured service with automatic fallback.
func ShortenURL(longURL string) string {
	return ShortenURLWithService(longURL, "")
}

// ShortenURLWithService shortens a URL using the specified service, with fallback chain.
func ShortenURLWithService(longURL string, preferredService string) string {
	if longURL == "" {
		return ""
	}

	// Try preferred service first
	if preferredService != "" {
		for _, svc := range shortenerServices {
			if svc.name == preferredService {
				shortURL, err := svc.fn(longURL)
				if err == nil {
					log.Printf("[RSS] Shortened URL using %s", svc.name)
					return shortURL
				}
				log.Printf("[RSS] Failed to shorten with %s: %v", svc.name, err)
				break
			}
		}
	}

	// Fallback chain: try all services
	for _, svc := range shortenerServices {
		shortURL, err := svc.fn(longURL)
		if err == nil {
			log.Printf("[RSS] Shortened URL using %s", svc.name)
			return shortURL
		}
		log.Printf("[RSS] Fallback %s failed: %v", svc.name, err)
	}

	log.Printf("[RSS] All shorteners failed, returning long URL")
	return longURL
}

// AvailableShorteners returns the list of available URL shortener service names.
func AvailableShorteners() []string {
	services := make([]string, len(shortenerServices))
	for i, svc := range shortenerServices {
		services[i] = svc.name
	}
	return services
}
