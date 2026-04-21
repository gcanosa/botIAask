package rss

import (
	"fmt"
	"log"
	"sync"
	"time"

	"botIAask/config"
	"github.com/mmcdole/gofeed"
	"io"
	"net/http"
	"net/url"
)

type BotInterface interface {
	Broadcast(channels []string, message string)
	IsConnected() bool
}

type Fetcher struct {
	cfg       *config.Config
	bot       BotInterface
	db        *Database
	mu        sync.Mutex
	enabled   bool
	stopChan  chan struct{}
	lastFetch time.Time
	lfMu      sync.RWMutex
}

func NewFetcher(cfg *config.Config, bot BotInterface, db *Database) *Fetcher {
	return &Fetcher{
		cfg:      cfg,
		bot:      bot,
		db:       db,
		enabled:  cfg.RSS.Enabled,
		stopChan: make(chan struct{}),
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

func (f *Fetcher) Fetch() {
	if !f.bot.IsConnected() {
		return
	}

	f.lfMu.Lock()
	f.lastFetch = time.Now()
	f.lfMu.Unlock()

	fp := gofeed.NewParser()
	var newEntries []NewsEntry

	for _, feedURL := range f.cfg.RSS.FeedURLs {
		feed, err := fp.ParseURL(feedURL)
		if err != nil {
			log.Printf("[RSS] Error fetching feed %s: %v", feedURL, err)
			continue
		}

		for _, item := range feed.Items {
			id := item.GUID
			if id == "" {
				id = item.Link
			}
			if id == "" {
				continue
			}

			seen, err := f.db.IsSeen(id)
			if err != nil {
				log.Printf("[RSS] DB Error: %v", err)
				continue
			}
			if !seen {
				pubDate := time.Now()
				if item.PublishedParsed != nil {
					pubDate = *item.PublishedParsed
				}
				entry := NewsEntry{
					GUID:    id,
					Title:   item.Title,
					Link:    item.Link,
					PubDate: pubDate,
				}
				newEntries = append(newEntries, entry)
			}
		}
	}

	// Send new entries to IRC with anti-spam delay
	// Sort by PubDate to send oldest first among the new ones
	// Actually we might want to sort all newEntries by PubDate if they come from different feeds

	for i := len(newEntries) - 1; i >= 0; i-- {
		entry := newEntries[i]

		// Shorten link and store it in entry
		entry.ShortLink = ShortenURL(entry.Link)

		// Mark as seen FIRST so we don't retry if broadcast fails for some reason
		if err := f.db.MarkSeen(entry); err != nil {
			log.Printf("[RSS] Failed to mark seen: %v", err)
			continue
		}

		if f.cfg.RSS.AnnounceToIRCEnabled() {
			// Short IRC format: "[NEWS] 15:04 - Title [🔗 Link]"
			msg := fmt.Sprintf("\x0304,01[NEWS]\x03 %s - %s", entry.PubDate.Format("15:04"), entry.Title)
			if entry.ShortLink != "" {
				msg += fmt.Sprintf(" \x0312\x1f🔗\x1f\x03 %s", entry.ShortLink)
			}
			f.bot.Broadcast(f.cfg.RSS.Channels, msg)
			time.Sleep(3 * time.Second)
		}
	}

	// Cleanup old entries
	retention := f.cfg.RSS.RetentionCount
	if retention <= 0 {
		retention = 50 // Default fallback
	}
	if err := f.db.Cleanup(retention); err != nil {
		log.Printf("[RSS] Cleanup Error: %v", err)
	}
}

// Backfill populates the database with the latest X items without broadcasting them.
func (f *Fetcher) Backfill(limit int) int {
	fp := gofeed.NewParser()
	count := 0

	for _, feedURL := range f.cfg.RSS.FeedURLs {
		if count >= limit {
			break
		}

		feed, err := fp.ParseURL(feedURL)
		if err != nil {
			log.Printf("[RSS] Error fetching feed %s for backfill: %v", feedURL, err)
			continue
		}

		log.Printf("[RSS] Feed %s fetched: %d items total", feedURL, len(feed.Items))

		for _, item := range feed.Items {
			if count >= limit {
				break
			}

			id := item.GUID
			if id == "" {
				id = item.Link
			}
			if id == "" {
				continue
			}

			exists, _, err := f.db.GetEntryStatus(id)
			if err != nil {
				log.Printf("[RSS] DB Error during backfill: %v", err)
				continue
			}

			if !exists {
				pubDate := time.Now()
				if item.PublishedParsed != nil {
					pubDate = *item.PublishedParsed
				}
				entry := NewsEntry{
					GUID:      id,
					Title:     item.Title,
					Link:      item.Link,
					ShortLink: ShortenURL(item.Link),
					PubDate:   pubDate,
				}
				if err := f.db.MarkSeen(entry); err != nil {
					log.Printf("[RSS] Failed to save backfill entry: %v", err)
					continue
				}
				count++
			}
		}
	}
	return count
}

func ShortenURL(longURL string) string {
	if longURL == "" {
		return ""
	}

	apiURL := fmt.Sprintf("https://is.gd/create.php?format=simple&url=%s", url.QueryEscape(longURL))

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("[RSS] Error shortening URL: %v", err)
		return longURL // Fallback to long URL
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[RSS] Shortener returned status: %s", resp.Status)
		return longURL
	}

	shortURL, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[RSS] Error reading shortener response: %v", err)
		return longURL
	}

	return string(shortURL)
}
