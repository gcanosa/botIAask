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
	f.mu.Unlock()

	ticker := time.NewTicker(time.Duration(f.cfg.RSS.IntervalMinutes) * time.Minute)
	defer ticker.Stop()

	// Wait for bot to be connected before initial fetch
	// Retry every 5 seconds for up to 2 minutes
	for i := 0; i < 24; i++ {
		if f.bot.IsConnected() {
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Initial fetch
	f.Fetch()

	for {
		select {
		case <-ticker.C:
			f.Fetch()
		case <-f.stopChan:
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
		go f.Start()
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

// ApplyConfig swaps in a new root config and restarts the fetch loop when RSS.Enabled or the ticker interval must change.
func (f *Fetcher) ApplyConfig(cfg *config.Config) {
	f.mu.Lock()
	f.cfg = cfg
	f.mu.Unlock()
	was := f.IsEnabled()
	if was {
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
	urls := f.cfg.RSS.FeedURLs
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

	f.lfMu.Lock()
	f.lastFetch = time.Now()
	f.lfMu.Unlock()

	fp := gofeed.NewParser()
	var newEntries []NewsEntry
	perFeed := make(map[string]lastFeedFetch, len(f.cfg.RSS.FeedURLs))

	for _, feedURL := range f.cfg.RSS.FeedURLs {
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
		entry.ShortLink = ShortenURLWithService(entry.Link, f.cfg.RSS.URLShortener)

		// Mark as seen FIRST so we don't retry if broadcast fails for some reason
		if err := f.db.MarkSeen(entry); err != nil {
			log.Printf("[RSS] Failed to mark seen: %v", err)
			continue
		}

		if f.cfg.RSS.AnnounceToIRCEnabled() {
			msg := FormatIRCNewsLine(entry, entry.ShortLink)
			f.bot.Broadcast(f.cfg.RSS.Channels, msg)
			time.Sleep(3 * time.Second)
		}
	}

	// Cleanup old entries
	retention := f.cfg.RSS.RetentionCount
	if retention <= 0 {
		retention = 50 // Default fallback
	}
	if err := f.db.CleanupPerSource(retention); err != nil {
		log.Printf("[RSS] Cleanup Error: %v", err)
	}
}

// Backfill populates the database with the latest X items without broadcasting them.
func (f *Fetcher) Backfill(limit int) int {
	fp := gofeed.NewParser()
	totalAdded := 0

	for _, feedURL := range f.cfg.RSS.FeedURLs {
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
			entry.ShortLink = ShortenURLWithService(entry.Link, f.cfg.RSS.URLShortener)
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

// doShorten performs a GET to apiURL and returns the plain-text short URL.
func doShorten(apiURL string) (string, error) {
	resp, err := http.Get(apiURL)
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
