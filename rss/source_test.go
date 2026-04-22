package rss

import (
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"
)

func TestSourceKeyFromSubscriptionURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://feeds.arstechnica.com/arstechnica/index", "arstechnica"},
		{"https://lobste.rs/rss", "lobste"},
		// news.ycombinator.com → registrable ycombinator.com → first label (same algorithm as HN, which overrides earlier)
		{"https://news.ycombinator.com/rss", "ycombinator"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sourceKeyFromSubscriptionURL(tt.in); got != tt.want {
				t.Errorf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestFeedSourceKeyFromFeed_CoversArs(t *testing.T) {
	f := &gofeed.Feed{Title: "Ars Technica - All content"}
	if got, want := FeedSourceKeyFromFeed("https://feeds.arstechnica.com/arstechnica/index", f), "arstechnica"; got != want {
		t.Errorf("FeedSourceKeyFromFeed(Ars): got %q want %q", got, want)
	}
}

func TestFeedSourceKeyFromFeed_StillHackerNews(t *testing.T) {
	if got, want := FeedSourceKeyFromFeed("https://news.ycombinator.com/rss", &gofeed.Feed{}), "hacker-news"; got != want {
		t.Errorf("HN: got %q want %q", got, want)
	}
}

func TestFeedSourceKeyFromFeed_NilFeedUsesRequestURL(t *testing.T) {
	if got, want := FeedSourceKeyFromFeed("https://feeds.arstechnica.com/arstechnica/index", nil), "arstechnica"; got != want {
		t.Errorf("nil feed: got %q want %q", got, want)
	}
}

func TestRegistrableDomainForFeedURL(t *testing.T) {
	if got, want := RegistrableDomainForFeedURL("https://feeds.arstechnica.com/x"), "arstechnica.com"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSourceIconForFeedURL(t *testing.T) {
	withImg := &gofeed.Feed{Image: &gofeed.Image{URL: "https://cdn.example.com/f.png"}}
	if got := SourceIconForFeedURL(withImg, "https://feeds.arstechnica.com/x"); got != "https://cdn.example.com/f.png" {
		t.Errorf("want feed image, got %q", got)
	}
	noImg := &gofeed.Feed{Title: "Ars Technica", Image: &gofeed.Image{URL: " "}}
	if got := SourceIconForFeedURL(noImg, "https://feeds.arstechnica.com/x"); !strings.HasPrefix(got, "https://www.google.com/s2/favicons?") {
		t.Errorf("want favicon URL, got %q", got)
	}
}
