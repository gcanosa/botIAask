package rss

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
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
}

func NewFetcher(cfg *config.Config, bot BotInterface, db *Database) *Fetcher {
	return &Fetcher{
		cfg:      cfg,
		bot:      bot,
		db:       db,
		enabled:  cfg.RSS.Enabled,
		stopChan: make(chan struct{}),
		feedLast: make(map[string]lastFeedFetch),
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

func (f *Fetcher) Fetch() {
	if !f.bot.IsConnected() {
		return
	}

	f.mu.Lock()
	cfg := f.cfg
	f.mu.Unlock()

	f.lfMu.Lock()
	f.lastFetch = time.Now()
	f.lfMu.Unlock()

	fp := gofeed.NewParser()
	fp.Client = feedHTTPClient
	var newEntries []NewsEntry
	perFeed := make(map[string]lastFeedFetch, len(cfg.RSS.FeedURLs))

	for _, feedURL := range cfg.RSS.FeedURLs {
		if feedURL == "" {
			continue
		}
		feed, err := fp.ParseURL(feedURL)
		at := time.Now()
		if err != nil {
			log.Printf("[RSS] Error fetching feed %s: %v", feedURL, err)
			perFeed[feedURL] = lastFeedFetch{OK: false, Err: err.Error(), Label: FeedLabelFallback(feedURL), At: at}
			continue
		}

		perFeed[feedURL] = lastFeedFetch{OK: true, Label: feedDisplayLabel(feedURL, feed), At: at}

		src := FeedSourceKeyFromFeed(feedURL, feed)
		srcIcon := SourceIconForFeedURL(feed, feedURL)
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
			if !dup {
				newEntries = append(newEntries, entry)
			}
		}
	}

	f.feedLastMu.Lock()
	f.feedLast = perFeed
	f.feedLastMu.Unlock()

	// Send new entries to IRC with anti-spam delay
	// Sort by PubDate to send oldest first among the new ones
	// Actually we might want to sort all newEntries by PubDate if they come from different feeds

	for i := len(newEntries) - 1; i >= 0; i-- {
		entry := newEntries[i]

		// Shorten link and store it in entry
		entry.ShortLink = ShortenURLWithService(entry.Link, cfg.RSS.URLShortener)

		// Mark as seen FIRST so we don't retry if broadcast fails for some reason
		if err := f.db.MarkSeen(entry); err != nil {
			log.Printf("[RSS] Failed to mark seen: %v", err)
			continue
		}

		if cfg.RSS.AnnounceToIRCEnabled() {
			msg := FormatIRCNewsLine(entry, entry.ShortLink)
			f.bot.Broadcast(cfg.RSS.Channels, msg)
			time.Sleep(3 * time.Second)
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
	f.mu.Lock()
	cfg := f.cfg
	f.mu.Unlock()

	fp := gofeed.NewParser()
	fp.Client = feedHTTPClient
	totalAdded := 0

	for _, feedURL := range cfg.RSS.FeedURLs {
		feed, err := fp.ParseURL(feedURL)
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
			entry.ShortLink = ShortenURLWithService(entry.Link, cfg.RSS.URLShortener)
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

var shortenerServices = []struct {
	name string
	fn   func(string) (string, error)
}{
	{"is.gd", shortenWithIsGd},
	{"tinyurl", shortenWithTinyURL},
	{"v.gd", shortenWithVGd},
	{"shorturl.at", shortenWithShortUrlAt},
	{"clck.ru", shortenWithClckRu},
	{"turl.at", shortenWithTurlAt},
	{"bc.vc", shortenWithBcVc},
	{"po.st", shortenWithPost},
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
	return doShorten(fmt.Sprintf("https://tinyurl.com/api/create.php?url=%s", url.QueryEscape(longURL)))
}

func shortenWithVGd(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://v.gd/?url=%s", url.QueryEscape(longURL)))
}

func shortenWithShortUrlAt(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://shorturl.at/api/links/shorten?url=%s", url.QueryEscape(longURL)))
}

func shortenWithClckRu(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://clck.ru/--?url=%s", url.QueryEscape(longURL)))
}

func shortenWithTurlAt(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://turl.at/new?url=%s", url.QueryEscape(longURL)))
}

func shortenWithBcVc(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://bc.vc/shorten?url=%s", url.QueryEscape(longURL)))
}

func shortenWithPost(longURL string) (string, error) {
	return doShorten(fmt.Sprintf("https://po.st/shorten?url=%s", url.QueryEscape(longURL)))
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
