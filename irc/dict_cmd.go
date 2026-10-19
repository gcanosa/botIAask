package irc

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"botIAask/internal/httpx"
	"botIAask/internal/ttlcache"
	"botIAask/meta"
)

// dictAPI is English Wiktionary (var so tests can point it at an httptest server). Only en.wiktionary
// serves the REST definition endpoint; it covers words of every language, keyed by language code.
// ponytail: glosses are always in English; native-language definitions would need es/it wikitext parsing.
var dictAPI = "https://en.wiktionary.org"

var dictCache = ttlcache.Cache[string, string]{TTL: 6 * time.Hour, Max: 256}

var (
	htmlTagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	errDictNoWord = errors.New("not found")
)

type dictEntry struct {
	PartOfSpeech string `json:"partOfSpeech"`
	Language     string `json:"language"`
	Definitions  []struct {
		Definition string `json:"definition"`
	} `json:"definitions"`
}

func fetchDict(ctx context.Context, word string) (map[string][]dictEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dictAPI+"/api/rest_v1/page/definition/"+url.PathEscape(word), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errDictNoWord
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r map[string][]dictEntry
	if err := httpx.DecodeJSON(resp.Body, httpx.MaxBody, &r); err != nil {
		return nil, err
	}
	return r, nil
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// formatDict: up to 2 parts of speech, 3 definitions each, plus the Wiktionary link.
func formatDict(word string, entries []dictEntry) string {
	var parts []string
	for _, e := range entries {
		var defs []string
		for _, d := range e.Definitions {
			// Drop nested example lists, then tags; Wiktionary leaves empty definitions for some templates.
			text := d.Definition
			if i := strings.Index(text, "<ul"); i >= 0 {
				text = text[:i]
			}
			text = strings.Join(strings.Fields(html.UnescapeString(htmlTagRe.ReplaceAllString(text, ""))), " ")
			if text != "" {
				defs = append(defs, cutRunes(text, 100))
			}
			if len(defs) == 3 {
				break
			}
		}
		if len(defs) > 0 {
			parts = append(parts, "\x02"+e.PartOfSpeech+"\x02: "+strings.Join(defs, "; "))
		}
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	lang := entries[0].Language
	link := dictAPI + "/wiki/" + url.PathEscape(strings.ReplaceAll(word, " ", "_")) + "#" + url.PathEscape(strings.ReplaceAll(lang, " ", "_"))
	return fmt.Sprintf("\x0303,01[DICT]\x03 \x02%s\x02 (%s) · %s · %s", word, lang, strings.Join(parts, " · "), link)
}

// handleDictCommand looks a word up in Wiktionary (!dict [lang:]<word>, lang = the word's language, default en).
func (b *ircNetwork) handleDictCommand(target, sender, query string) {
	lang := "en"
	if m := wikiLangRe.FindStringSubmatch(query); m != nil {
		lang, query = m[1], strings.TrimSpace(m[2])
	}
	key := lang + ":" + query
	if line, ok := dictCache.Get(key); ok {
		b.sendPrivmsg(target, b.sanitize(line))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Wiktionary titles are case-sensitive: try as typed, then lower-cased ("Casa" → "casa").
	word := query
	r, err := fetchDict(ctx, word)
	if lw := strings.ToLower(query); (errors.Is(err, errDictNoWord) || (err == nil && len(r[lang]) == 0)) && lw != query {
		word = lw
		r, err = fetchDict(ctx, word)
	}
	line := ""
	if err == nil {
		line = formatDict(word, r[lang])
		if line == "" {
			err = errDictNoWord
		}
	}
	if err != nil {
		if errors.Is(err, errDictNoWord) {
			err = fmt.Errorf("no %s definition for %q", lang, query)
		}
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: dict: %v", sender, err)))
		return
	}
	dictCache.Put(key, line)
	b.sendPrivmsg(target, b.sanitize(line))
}
