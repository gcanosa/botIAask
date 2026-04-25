package rss

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/mmcdole/gofeed"
	"golang.org/x/net/publicsuffix"
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
	if f != nil {
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
	}
	if k := sourceKeyFromSubscriptionURL(requestURL); k != "" {
		return k
	}
	if f != nil {
		for _, u := range []string{f.FeedLink, f.Link} {
			if k := sourceKeyFromSubscriptionURL(u); k != "" {
				return k
			}
		}
	}
	return ""
}

// sourceKeyFromSubscriptionURL derives a stable key from the feed’s own URL
// (e.g. https://feeds.arstechnica.com/... → "arstechnica") using the registrable domain.
func sourceKeyFromSubscriptionURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	// e.g. "feeds.arstechnica.com" → "arstechnica.com"
	eTLD1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || eTLD1 == "" {
		// e.g. "localhost" or other non-ICANN hosts
		eTLD1 = host
	}
	i := strings.IndexByte(eTLD1, '.')
	var label string
	if i > 0 {
		label = eTLD1[:i]
	} else {
		label = eTLD1
	}
	return normalizeSourceKey(label)
}

// FeedLabelFallback is a display name for a feed when the feed document could not be parsed.
func FeedLabelFallback(feedURL string) string {
	if k := FeedSourceKey(feedURL); k == "hacker-news" {
		return "Hacker News"
	}
	d := RegistrableDomainForFeedURL(feedURL)
	if d == "arstechnica.com" {
		return "Ars Technica"
	}
	if d != "" {
		part := d
		if i := strings.IndexByte(part, '.'); i > 0 {
			part = part[:i]
		}
		if len(part) > 0 {
			return strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return feedURL
}

func normalizeSourceKey(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastHyphen := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
			lastHyphen = false
		} else {
			// treat separators as single hyphen, collapse
			if b.Len() == 0 || lastHyphen {
				continue
			}
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	k := strings.Trim(b.String(), "-")
	if k == "" {
		return ""
	}
	return k
}

// RegistrableDomainForFeedURL returns the eTLD+1 hostname (e.g. "arstechnica.com") for favicon resolution.
// Empty string if the URL has no host.
func RegistrableDomainForFeedURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	eTLD1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || eTLD1 == "" {
		return host
	}
	return eTLD1
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
