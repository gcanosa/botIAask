package irc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
	"botIAask/internal/ttlcache"
)

// ytWatch is the watch-page URL template (var so tests can point it at an httptest server).
var ytWatch = "https://www.youtube.com/watch?v=%s"

var (
	ytURLRe = regexp.MustCompile(`(?i)https?://\S+`)
	ytIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	ytCache = ttlcache.Cache[string, *ytVideo]{TTL: 10 * time.Minute, Max: 256}
	// ytSeen stops a link pasted twice in a row (or by several people) from being announced again.
	ytSeen = ttlcache.Cache[string, bool]{TTL: 2 * time.Minute, Max: 256}
)

type ytVideo struct {
	Title, Channel  string
	Length          time.Duration
	Views, Likes    int64
	Published       time.Time
	Live, HaveLikes bool
}

// youtubeLink returns the video id and the ?t= start offset of the first YouTube link in msg.
func youtubeLink(msg string) (id string, start time.Duration, ok bool) {
	for _, raw := range ytURLRe.FindAllString(msg, 3) {
		u, err := url.Parse(strings.TrimRight(raw, ".,;:!?)>\"'"))
		if err != nil {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		path := strings.Split(strings.Trim(u.Path, "/"), "/")
		switch {
		case host == "youtu.be":
			id = path[0]
		case host == "youtube.com" || host == "m.youtube.com" || host == "music.youtube.com":
			if path[0] == "watch" {
				id = u.Query().Get("v")
			} else if len(path) == 2 && (path[0] == "shorts" || path[0] == "live" || path[0] == "embed") {
				id = path[1]
			}
		}
		if !ytIDRe.MatchString(id) {
			id = ""
			continue
		}
		return id, ytStart(u.Query().Get("t")), true
	}
	return "", 0, false
}

// ytStart parses "1340", "1340s" or "22m20s" (YouTube accepts all three).
func ytStart(t string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSuffix(t, "s")); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if d, err := time.ParseDuration(t); err == nil && d > 0 {
		return d
	}
	return 0
}

// fetchYouTube scrapes ytInitialPlayerResponse from the watch page (no API key needed).
// ponytail: page scrape; breaks if YouTube renames these fields — switch to the Data API (needs a key) then.
func fetchYouTube(ctx context.Context, id string) (*ytVideo, error) {
	if v, ok := ytCache.Get(id); ok {
		return v, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(ytWatch, id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en")
	req.Header.Set("Cookie", "SOCS=CAI") // skips the EU cookie-consent interstitial
	resp, err := latestHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	const marker = "ytInitialPlayerResponse = "
	i := bytes.Index(page, []byte(marker))
	if i < 0 {
		return nil, fmt.Errorf("no player response")
	}
	var p struct {
		VideoDetails struct {
			Title         string `json:"title"`
			Author        string `json:"author"`
			LengthSeconds string `json:"lengthSeconds"`
			ViewCount     string `json:"viewCount"`
			IsLive        bool   `json:"isLive"`
		} `json:"videoDetails"`
		Microformat struct {
			R struct {
				LikeCount   string `json:"likeCount"`
				PublishDate string `json:"publishDate"`
			} `json:"playerMicroformatRenderer"`
		} `json:"microformat"`
	}
	// The decoder stops after the one JSON object, ignoring the "; var ..." script that follows.
	if err := json.NewDecoder(bytes.NewReader(page[i+len(marker):])).Decode(&p); err != nil {
		return nil, err
	}
	d, m := p.VideoDetails, p.Microformat.R
	if d.Title == "" {
		return nil, fmt.Errorf("video unavailable")
	}
	v := &ytVideo{Title: d.Title, Channel: d.Author, Live: d.IsLive}
	secs, _ := strconv.ParseInt(d.LengthSeconds, 10, 64)
	v.Length = time.Duration(secs) * time.Second
	v.Views, _ = strconv.ParseInt(d.ViewCount, 10, 64)
	v.Likes, err = strconv.ParseInt(m.LikeCount, 10, 64)
	v.HaveLikes = err == nil
	v.Published, _ = time.Parse(time.RFC3339, m.PublishDate)
	ytCache.Put(id, v)
	return v, nil
}

// clock renders 58:46 or 1:02:03.
func clock(d time.Duration) string {
	s := int64(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// compactCount renders 145501 as 145.5K.
func compactCount(n int64) string {
	switch {
	case n >= 1e9:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + "B"
	case n >= 1e6:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 1e3:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func formatYouTube(v *ytVideo, start time.Duration) string {
	parts := []string{"\x0301,00You\x0300,04Tube\x0F \x02" + v.Title + "\x02"}
	if start > 0 {
		parts = append(parts, "@"+clock(start))
	}
	switch {
	case v.Live:
		parts = append(parts, "\x0304LIVE\x03")
	case v.Length > 0:
		parts = append(parts, clock(v.Length))
	}
	if v.Channel != "" {
		parts = append(parts, "Channel: \x02"+v.Channel+"\x02")
	}
	parts = append(parts, compactCount(v.Views)+" views")
	if v.HaveLikes {
		parts = append(parts, "👍 "+compactCount(v.Likes))
	}
	if !v.Published.IsZero() {
		parts = append(parts, humanizeSince(v.Published)+" ago")
	}
	return strings.Join(parts, " | ")
}

// announceYouTube posts video info for the first YouTube link in a channel message. Passive, so it
// never replies on failure or rate limit: it just stays quiet.
func (b *ircNetwork) announceYouTube(channel, sender, msg string) {
	id, start, ok := youtubeLink(msg)
	if !ok {
		return
	}
	key := b.name + "\x00" + strings.ToLower(channel) + "\x00" + id
	if _, dup := ytSeen.Get(key); dup {
		return
	}
	if rl, cfg := b.limiter(), b.getCfg().Bot.RateLimiting; rl != nil && cfg != nil && !rl.Allow(b.name, sender, "youtube", cfg.Burst) {
		return
	}
	select { // same slots as commands, so a link flood can't spawn unbounded fetches
	case b.cmdSem <- struct{}{}:
	default:
		return
	}
	ytSeen.Put(key, true)
	guard.Go("youtube", func() {
		defer func() { <-b.cmdSem }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		v, err := fetchYouTube(ctx, id)
		if err != nil {
			log.Printf("irc[%s]: youtube %s: %v", b.name, id, err)
			return
		}
		b.sendPrivmsg(channel, b.sanitize(formatYouTube(v, start)))
	})
}

// handleYouTubeCommand: !youtube [on|off] [#chan|*] (admin session required, see bot.go).
func (b *ircNetwork) handleYouTubeCommand(target string, args []string) {
	usage := fmt.Sprintf("Usage: %syoutube [on|off] [#chan|*]", b.pfx())
	if len(args) == 0 {
		if on := b.netCfg().YouTubePreview; len(on) > 0 {
			b.sendPrivmsg(target, "YouTube previews on "+b.name+": "+strings.Join(on, ", "))
		} else {
			b.sendPrivmsg(target, "YouTube previews are off on "+b.name+".")
		}
		return
	}
	enable := strings.EqualFold(args[0], "on")
	if !enable && !strings.EqualFold(args[0], "off") || len(args) > 2 {
		b.sendPrivmsg(target, usage)
		return
	}
	channel := target
	if len(args) == 2 {
		channel = args[1]
	}
	if channel != "*" && !ircChannelTarget(channel) {
		b.sendPrivmsg(target, usage)
		return
	}
	err := b.editNetwork("!youtube", func(n *config.IRCNetworkConfig) error {
		n.YouTubePreview = slices.DeleteFunc(n.YouTubePreview, func(c string) bool { return strings.EqualFold(c, channel) })
		if enable {
			n.YouTubePreview = append(n.YouTubePreview, channel)
		}
		return nil
	})
	if err != nil {
		b.sendPrivmsg(target, "youtube: "+err.Error())
		return
	}
	where := channel
	if channel == "*" {
		where = "all channels on " + b.name
	}
	b.sendPrivmsg(target, fmt.Sprintf("YouTube previews %s for %s.", args[0], where))
}
