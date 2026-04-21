package bookmarks

import (
	"net/url"
	"strings"
)

// ValidBookmarkURL reports whether raw is an absolute http(s) URL with a host.
func ValidBookmarkURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		return true
	default:
		return false
	}
}

// bookmarkURLLikePattern returns a LIKE pattern matching URLs containing substr, with % and _ escaped for ESCAPE '\'.
func bookmarkURLLikePattern(substr string) string {
	var b strings.Builder
	b.Grow(len(substr) + 2)
	b.WriteByte('%')
	for _, r := range substr {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}
