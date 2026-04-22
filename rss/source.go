package rss

import (
	"net/url"
	"strings"

	"github.com/mmcdole/gofeed"
)

// FeedSourceKey returns a stable key for UI (e.g. "hacker-news") from the subscription / feed URL only
// (not from individual item links). Uses host, path, and query.
func FeedSourceKey(feedURL string) string {
	u, err := url.Parse(feedURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.Path)
	q := strings.ToLower(u.RawQuery)
	combined := path + "?" + q

	if h == "news.ycombinator.com" || strings.HasSuffix(h, ".ycombinator.com") {
		return "hacker-news"
	}
	if h == "hnrss.org" || strings.HasSuffix(h, ".hnrss.org") {
		return "hacker-news"
	}
	// RSSHub, proxies, etc.: path/query often contain the route
	if strings.Contains(combined, "ycombinator") {
		return "hacker-news"
	}
	if strings.Contains(path, "hackernews") || strings.Contains(combined, "hacker-news") {
		return "hacker-news"
	}
	return ""
}

// FeedSourceKeyFromFeed resolves the key from the request URL plus parsed feed metadata.
// Item links often point off-site; the feed's own Link/FeedLink/title identify the source (e.g. HN).
func FeedSourceKeyFromFeed(requestURL string, f *gofeed.Feed) string {
	if k := FeedSourceKey(requestURL); k != "" {
		return k
	}
	if f == nil {
		return ""
	}
	for _, u := range []string{f.FeedLink, f.Link} {
		if strings.TrimSpace(u) == "" {
			continue
		}
		if k := FeedSourceKey(u); k != "" {
			return k
		}
	}
	for _, u := range f.Links {
		if k := FeedSourceKey(u); k != "" {
			return k
		}
	}
	t := strings.ToLower(strings.TrimSpace(f.Title))
	if t == "hacker news" || strings.HasPrefix(t, "hacker news:") {
		return "hacker-news"
	}
	return ""
}

// RepairEmptySourceHackerNewsWhenSingleHNFeed sets source for legacy rows (empty source) when the only
// configured feed is Hacker News—safe because those rows predate the source column.
func (d *Database) RepairEmptySourceHackerNewsWhenSingleHNFeed(feedURLs []string) error {
	if len(feedURLs) != 1 {
		return nil
	}
	if FeedSourceKey(feedURLs[0]) != "hacker-news" {
		return nil
	}
	_, err := d.db.Exec(`UPDATE seen_news SET source = 'hacker-news' WHERE source IS NULL OR source = ''`)
	return err
}
