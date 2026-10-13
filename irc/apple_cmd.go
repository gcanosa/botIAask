package irc

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"botIAask/meta"
)

// appleReleasesFeed is Apple's official developer release feed (vars so tests can swap it).
var appleReleasesFeed = "https://developer.apple.com/news/releases/rss/releases.rss"

// appleOSes is the display order with a per-OS icon.
var appleOSes = []struct{ name, icon string }{
	{"macOS", "💻"}, {"iOS", "📱"}, {"iPadOS", "📲"}, {"watchOS", "⌚"}, {"tvOS", "📺"}, {"visionOS", "🥽"},
}

// Stable releases only: "iOS 27.0.1 (24A446)". Betas/RCs ("27.2 beta 3 (…)") don't match.
var appleReleaseRe = regexp.MustCompile(`^(macOS|iOS|iPadOS|watchOS|tvOS|visionOS)(?: [A-Za-z]+)? (\d+(?:\.\d+)*) \((\w+)\)$`)

type appleRelease struct{ version, build, name string }

// macOSNames maps a macOS major version to its marketing name (only the newest is ever shown).
// ponytail: static table; a major missing here is looked up on apple.com/macos (macOSNameFromApple).
var macOSNames = map[string]string{
	"11": "Big Sur", "12": "Monterey", "13": "Ventura", "14": "Sonoma",
	"15": "Sequoia", "26": "Tahoe", "27": "Golden Gate",
}

var appleMacOSPage = "https://www.apple.com/macos/"

// apple.com/macos <title> reads "OS - macOS 27 Golden Gate - Apple".
var macOSTitleRe = regexp.MustCompile(`macOS (\d+) ([A-Z][A-Za-z ]+?) -`)

// macOSNameFromApple returns the marketing name for major from apple.com/macos, or "" if unknown.
func macOSNameFromApple(ctx context.Context, major string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appleMacOSPage, nil)
	if err != nil {
		return ""
	}
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if m := macOSTitleRe.FindSubmatch(body); m != nil && string(m[1]) == major {
		return string(m[2])
	}
	return ""
}

// appleVersionLess compares dotted numeric versions ("26.7.1" < "27.0").
func appleVersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// latestAppleReleases picks the highest stable version per OS from feed item titles.
// The feed is date-ordered and older branches still get patches (iPadOS 26.7.1 after 27.0.1), hence max, not first.
func latestAppleReleases(titles []string) map[string]appleRelease {
	out := map[string]appleRelease{}
	for _, t := range titles {
		m := appleReleaseRe.FindStringSubmatch(strings.TrimSpace(t))
		if m == nil {
			continue
		}
		if cur, ok := out[m[1]]; !ok || appleVersionLess(cur.version, m[2]) {
			out[m[1]] = appleRelease{version: m[2], build: m[3]}
		}
	}
	return out
}

func fetchAppleReleases(ctx context.Context) (map[string]appleRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appleReleasesFeed, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version)
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apple feed: HTTP %d", resp.StatusCode)
	}
	var feed struct {
		Items []struct {
			Title string `xml:"title"`
		} `xml:"channel>item"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("apple feed: %w", err)
	}
	titles := make([]string, len(feed.Items))
	for i, it := range feed.Items {
		titles[i] = it.Title
	}
	r := latestAppleReleases(titles)
	if mac, ok := r["macOS"]; ok {
		major, _, _ := strings.Cut(mac.version, ".")
		if mac.name = macOSNames[major]; mac.name == "" {
			mac.name = macOSNameFromApple(ctx, major)
		}
		r["macOS"] = mac
	}
	return r, nil
}

// formatAppleReleases follows the [TAG] · separated style of !latest/!movie.
func formatAppleReleases(r map[string]appleRelease) string {
	var parts []string
	for _, o := range appleOSes {
		if v, ok := r[o.name]; ok {
			name := o.name
			if v.name != "" {
				name += " " + v.name
			}
			parts = append(parts, fmt.Sprintf("%s %s %s%s%s (%s)", o.icon, name, ircBold, v.version, ircBold, v.build))
		}
	}
	return "\x0303,01[APPLE]\x03 " + strings.Join(parts, " · ")
}

// handleAppleCommand reports the newest stable release of each Apple OS (!apple).
func (b *ircNetwork) handleAppleCommand(target, sender string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r, err := fetchAppleReleases(ctx)
	if err == nil && len(r) == 0 {
		err = fmt.Errorf("no stable releases in Apple's feed")
	}
	if err != nil {
		b.sendPrivmsg(target, b.sanitize(fmt.Sprintf("@%s: apple: %v", sender, err)))
		return
	}
	b.sendPrivmsg(target, b.sanitize(formatAppleReleases(r)))
}
