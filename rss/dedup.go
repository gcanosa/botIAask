package rss

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

var trackingQueryKeys = map[string]struct{}{
	"utm_source":   {},
	"utm_medium":   {},
	"utm_campaign": {},
	"utm_term":     {},
	"utm_content":  {},
	"utm_id":       {},
	"fbclid":       {},
	"gclid":        {},
	"mc_cid":       {},
	"mc_eid":       {},
	"_ga":          {},
	"igshid":       {},
}

// NormalizeRSSLink returns a canonical form for deduplication: stable host, no fragment,
// no common tracking query params, sorted query keys, optional https upgrade, trimmed trailing slash on path.
func NormalizeRSSLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Hostname())
	u.Fragment = ""
	u.RawPath = ""
	u.Path = strings.TrimSpace(u.Path)
	if u.Path == "" {
		u.Path = "/"
	}
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if _, drop := trackingQueryKeys[lk]; drop {
			q.Del(k)
		}
	}
	u.RawQuery = encodeSortedQuery(q)
	return u.String()
}

func encodeSortedQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			if !first {
				b.WriteByte('&')
			}
			first = false
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}

// DedupKeyFromParts returns a stable hex sha256 for (source + normalized link / guid / title).
func DedupKeyFromParts(sourceKey, linkNormalized, guid, title string) string {
	sourceKey = strings.TrimSpace(sourceKey)
	linkNormalized = strings.TrimSpace(linkNormalized)
	guid = strings.TrimSpace(guid)
	title = strings.TrimSpace(title)
	var payload string
	switch {
	case linkNormalized != "":
		payload = sourceKey + "\x00" + linkNormalized
	case guid != "":
		payload = sourceKey + "\x00g:" + guid
	default:
		payload = sourceKey + "\x00t:" + title
	}
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// PrimaryGUIDForRSSItem chooses a row primary key: publisher GUID, else normalized link, else "d:"+dedupKey.
func PrimaryGUIDForRSSItem(guid, rawLink, linkNorm, dedupKey string) string {
	if g := strings.TrimSpace(guid); g != "" {
		return g
	}
	if linkNorm != "" {
		return linkNorm
	}
	if raw := strings.TrimSpace(rawLink); raw != "" {
		if n := NormalizeRSSLink(raw); n != "" {
			return n
		}
	}
	if dedupKey != "" {
		return "d:" + dedupKey
	}
	return ""
}

// EntryFromFeedItem builds a NewsEntry with dedup fields; returns ok false if the item has no stable identity.
func EntryFromFeedItem(item *gofeed.Item, src, srcIcon string) (NewsEntry, bool) {
	if item == nil {
		return NewsEntry{}, false
	}
	linkNorm := NormalizeRSSLink(item.Link)
	dedup := DedupKeyFromParts(src, linkNorm, item.GUID, item.Title)
	pk := PrimaryGUIDForRSSItem(item.GUID, item.Link, linkNorm, dedup)
	if pk == "" {
		return NewsEntry{}, false
	}
	pubDate := time.Now()
	if item.PublishedParsed != nil {
		pubDate = *item.PublishedParsed
	}
	return NewsEntry{
		GUID:           pk,
		Title:          item.Title,
		Link:           item.Link,
		PubDate:        pubDate,
		Source:         src,
		SourceIcon:     srcIcon,
		LinkNormalized: linkNorm,
		DedupKey:       dedup,
	}, true
}
