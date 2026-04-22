package rss

import (
	"net/url"
	"strings"

	"github.com/mmcdole/gofeed"
)

// SourceIconForFeedURL returns a URL for the web badge: the feed’s channel image when present,
// otherwise a favicon URL derived from the subscription URL’s registrable domain.
func SourceIconForFeedURL(feed *gofeed.Feed, feedURL string) string {
	if feed != nil && feed.Image != nil {
		if u := strings.TrimSpace(feed.Image.URL); u != "" {
			return u
		}
	}
	dom := RegistrableDomainForFeedURL(feedURL)
	if dom == "" {
		return ""
	}
	// No feed image: stable remote favicon for the site (avoids mixed-content for https dashboard).
	return "https://www.google.com/s2/favicons?domain=" + url.QueryEscape(dom) + "&sz=32"
}
