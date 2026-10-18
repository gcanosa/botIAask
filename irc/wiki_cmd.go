package irc

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"botIAask/internal/httpx"
	"botIAask/internal/ttlcache"
	"botIAask/meta"
)

// wikiAPI is the MediaWiki API host template (var so tests can point it at an httptest server).
var wikiAPI = "https://%s.wikipedia.org"

// wikiLangRe matches an optional language prefix: "!wiki es:argentina".
var wikiLangRe = regexp.MustCompile(`^([a-z]{2,3}):(.+)$`)

var wikiCache = ttlcache.Cache[string, string]{TTL: 30 * time.Minute, Max: 256}

type wikiPage struct {
	Title       string         `json:"title"`
	Index       int            `json:"index"`
	Description string         `json:"description"`
	PageProps   map[string]any `json:"pageprops"`
}

// fetchWiki runs a ranked full-text search and returns the top 4 hits in rank order.
func fetchWiki(ctx context.Context, base, query string) ([]wikiPage, error) {
	v := url.Values{
		"action": {"query"}, "format": {"json"}, "formatversion": {"2"},
		"generator": {"search"}, "gsrsearch": {query}, "gsrlimit": {"4"},
		"prop": {"description|pageprops"}, "ppprop": {"disambiguation"}, "redirects": {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/w/api.php?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Query struct {
			Pages []wikiPage `json:"pages"`
		} `json:"query"`
	}
	if err := httpx.DecodeJSON(resp.Body, httpx.MaxBody, &r); err != nil {
		return nil, err
	}
	pages := r.Query.Pages
	slices.SortFunc(pages, func(a, b wikiPage) int { return a.Index - b.Index })
	return pages, nil
}

func wikiURL(base, title string) string {
	return base + "/wiki/" + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
}

// formatWiki: top article + description + link, other hits as "also:". When the top hit is a
// disambiguation page the query is ambiguous, so list the candidates and link the disambiguation page.
func formatWiki(base string, pages []wikiPage) string {
	var others []string
	for _, p := range pages[1:] {
		if _, dis := p.PageProps["disambiguation"]; !dis {
			others = append(others, p.Title)
		}
	}
	top := pages[0]
	if _, dis := top.PageProps["disambiguation"]; dis && len(others) > 0 {
		return fmt.Sprintf("\x0303,01[WIKI]\x03 \x02%s\x02 is ambiguous: %s · %s", top.Title, strings.Join(others, ", "), wikiURL(base, top.Title))
	}
	line := "\x0303,01[WIKI]\x03 \x02" + top.Title + "\x02"
	if top.Description != "" {
		line += " — " + top.Description
	}
	line += " · " + wikiURL(base, top.Title)
	if len(others) > 0 {
		line += " · also: " + strings.Join(others, ", ")
	}
	return line
}

// handleWikiCommand searches Wikipedia and replies with the best article's URL (!wiki [lang:]<query>).
func (b *ircNetwork) handleWikiCommand(target, sender, query string) {
	lang := "en"
	if m := wikiLangRe.FindStringSubmatch(query); m != nil {
		lang, query = m[1], strings.TrimSpace(m[2])
	}
	key := lang + ":" + strings.ToLower(query)
	if line, ok := wikiCache.Get(key); ok {
		b.sendPrivmsg(target, b.sanitize(line))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	base := fmt.Sprintf(wikiAPI, lang)
	pages, err := fetchWiki(ctx, base, query)
	if err == nil && len(pages) == 0 {
		err = fmt.Errorf("no results for %q", query)
	}
	if err != nil {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: wiki: %v", sender, err)))
		return
	}
	line := formatWiki(base, pages)
	wikiCache.Put(key, line)
	b.sendPrivmsg(target, b.sanitize(line))
}
