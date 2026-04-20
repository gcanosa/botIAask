package rss

import (
	"fmt"
	"log"
	"sync"
	"time"

	"botIAask/config"
	"github.com/mmcdole/gofeed"
	"net/http"
	"io"
	"net/url"
)

type BotInterface interface {
	Broadcast(channels []string, message string)
	IsConnected() bool
}

type Fetcher struct {
	cfg      *config.Config
	bot      BotInterface
	db       *Database
	mu       sync.Mutex
	enabled  bool
	stopChan chan struct{}
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

func (f *Fetcher) Fetch() {
	if !f.bot.IsConnected() {
		return
	}

	fp := gofeed.NewParser()
	feed, err := fp.ParseURL("https://news.ycombinator.com/rss")
	if err != nil {
		log.Printf("[RSS] Error fetching feed: %v", err)
		return
	}

	var newEntries []NewsEntry
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

	// Send new entries to IRC with anti-spam delay
	// Hacker News RSS usually has 30 items. If we just enabled it, we might have many.
	// We only send the ones we haven't seen.
	
	// Sort by PubDate to send oldest first among the new ones
	// Actually gofeed usually gives them newest first.
	
	for i := len(newEntries) - 1; i >= 0; i-- {
		entry := newEntries[i]
		
		// Shorten link and store it in entry
		entry.ShortLink = ShortenURL(entry.Link)

		// Mark as seen FIRST so we don't retry if broadcast fails for some reason
		if err := f.db.MarkSeen(entry); err != nil {
			log.Printf("[RSS] Failed to mark seen: %v", err)
			continue
		}

		// Short IRC format: "[NEWS] 15:04 - Title [🔗 Link]"
		msg := fmt.Sprintf("\x0304,01[NEWS]\x03 %s - %s", entry.PubDate.Format("15:04"), entry.Title)
		if entry.ShortLink != "" {
			msg += fmt.Sprintf(" \x0312\x1f🔗\x1f\x03 %s", entry.ShortLink)
		}
		f.bot.Broadcast(f.cfg.RSS.Channels, msg)

		// Anti-spam delay
		time.Sleep(3 * time.Second)
	}

	// Cleanup old entries
	if err := f.db.Cleanup(500); err != nil {
		log.Printf("[RSS] Cleanup Error: %v", err)
	}
}

// Backfill populates the database with the latest X items without broadcasting them.
func (f *Fetcher) Backfill(limit int) int {
	fp := gofeed.NewParser()
	feed, err := fp.ParseURL("https://news.ycombinator.com/rss")
	if err != nil {
		log.Printf("[RSS] Error fetching feed for backfill: %v", err)
		return 0
	}

	log.Printf("[RSS] Feed fetched: %d items total", len(feed.Items))

	count := 0
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
