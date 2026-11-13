// Package reddit announces new posts of followed subreddits to IRC. It reads the public
// /r/<sub>/new/.rss feed (no API key), dedups by post id in data/reddit_seen.db and
// announces oldest first, each post once, to the channels configured per subreddit.
package reddit

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
	"botIAask/internal/httpx"
	"botIAask/meta"

	"github.com/mmcdole/gofeed"
)

const (
	maxAnnouncePerCycle = 10 // the rest stays unseen and goes out next cycle
	firstPollAnnounce   = 3  // a newly followed sub announces only its newest posts
	maxBody             = 2 << 20
	requestGap          = 2 * time.Second // between announcements
)

// errBlocked: Reddit answered 403/429 or its HTML bot check; retried next cycle.
var errBlocked = errors.New("blocked or rate limited by Reddit, retrying next cycle")

// BotInterface is what the fetcher needs from *irc.Bot.
type BotInterface interface {
	Broadcast(channels []string, message string)
	IsConnected() bool
}

// Post is one subreddit post from the feed.
type Post struct {
	Sub, ID, Title, Author, Link string
	Published                    time.Time
}

// SubStatus is the last poll result of one subreddit, for the dashboard and !reddit list.
type SubStatus struct {
	Name         string     `json:"name"`
	OK           bool       `json:"ok"`
	Error        string     `json:"error,omitempty"`
	LastPolledAt *time.Time `json:"last_polled_at,omitempty"`
}

type Fetcher struct {
	mu       sync.Mutex
	cfg      *config.Config
	bot      BotInterface
	db       *Database
	enabled  bool
	stopChan chan struct{}

	fetchMu sync.Mutex // one Fetch at a time, so a post is never announced twice

	statusMu sync.RWMutex
	status   map[string]SubStatus // lower-case sub name

	client  *http.Client
	baseURL string        // https://www.reddit.com; a test server in tests
	gap     time.Duration // requestGap; 0 in tests
}

func NewFetcher(cfg *config.Config, bot BotInterface, db *Database) *Fetcher {
	return &Fetcher{
		cfg: cfg, bot: bot, db: db,
		stopChan: make(chan struct{}),
		status:   map[string]SubStatus{},
		client:   &http.Client{Timeout: 15 * time.Second},
		baseURL:  "https://www.reddit.com",
		gap:      requestGap,
	}
}

func (f *Fetcher) config() *config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *Fetcher) Start() {
	f.mu.Lock()
	if !f.enabled {
		f.mu.Unlock()
		return
	}
	stop := f.stopChan // captured once: Stop installs a fresh channel
	interval := f.cfg.Reddit.IntervalMinutes
	f.mu.Unlock()
	if interval <= 0 {
		interval = config.RedditDefaultInterval
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Minute)
	defer ticker.Stop()

	for i := 0; i < 24 && !f.bot.IsConnected(); i++ {
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
	f.Fetch()
	for {
		select {
		case <-ticker.C:
			f.Fetch()
		case <-stop:
			return
		}
	}
}

func (f *Fetcher) Stop() { f.SetEnabled(false) }

func (f *Fetcher) SetEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enabled == enabled {
		return
	}
	f.enabled = enabled
	if enabled {
		guard.Go("reddit", f.Start)
		return
	}
	close(f.stopChan)
	f.stopChan = make(chan struct{})
}

func (f *Fetcher) IsEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

// ApplyConfig swaps the config; the loop restarts only when enabled or the interval changed.
func (f *Fetcher) ApplyConfig(cfg *config.Config) {
	f.mu.Lock()
	same := f.enabled == cfg.Reddit.Enabled && f.cfg.Reddit.IntervalMinutes == cfg.Reddit.IntervalMinutes
	f.cfg = cfg
	f.mu.Unlock()
	if same {
		return
	}
	f.SetEnabled(false)
	f.SetEnabled(cfg.Reddit.Enabled)
}

// Statuses returns one row per configured subreddit, in config order.
func (f *Fetcher) Statuses() []SubStatus {
	subs := f.config().Reddit.Subreddits
	f.statusMu.RLock()
	defer f.statusMu.RUnlock()
	out := make([]SubStatus, 0, len(subs))
	for _, s := range subs {
		st, ok := f.status[strings.ToLower(s.Name)]
		if !ok {
			st = SubStatus{Error: "not yet polled"}
		}
		st.Name = s.Name
		out = append(out, st)
	}
	return out
}

func (f *Fetcher) setStatus(name string, err error) {
	now := time.Now()
	st := SubStatus{Name: name, OK: err == nil, LastPolledAt: &now}
	if err != nil {
		st.Error = err.Error()
	}
	f.statusMu.Lock()
	f.status[strings.ToLower(name)] = st
	f.statusMu.Unlock()
}

// Fetch polls every subreddit once and announces the unseen posts, oldest first.
func (f *Fetcher) Fetch() {
	if !f.bot.IsConnected() || !f.fetchMu.TryLock() {
		return
	}
	defer f.fetchMu.Unlock()
	cfg := f.config()

	subs := cfg.Reddit.Subreddits
	if len(subs) == 0 {
		return
	}
	// One request for every followed sub (/r/a+b+c): Reddit rate-limits anonymous clients per
	// IP, and one request per sub got the second one refused.
	names := make([]string, len(subs))
	for i, s := range subs {
		names[i] = s.Name
	}
	posts, err := f.fetchFeed(names)
	for _, s := range subs {
		f.setStatus(s.Name, err)
	}
	if err != nil {
		log.Printf("[REDDIT] %s: %v", strings.Join(names, "+"), err)
		return
	}
	bySub := map[string][]Post{}
	for _, p := range posts {
		if i := config.FindRedditSub(subs, p.Sub); i >= 0 {
			p.Sub = subs[i].Name
			bySub[strings.ToLower(p.Sub)] = append(bySub[strings.ToLower(p.Sub)], p)
		}
	}
	var fresh []Post
	for _, s := range subs {
		unseen, err := f.unseen(s.Name, bySub[strings.ToLower(s.Name)])
		if err != nil {
			log.Printf("[REDDIT] r/%s: %v", s.Name, err)
			continue
		}
		fresh = append(fresh, unseen...)
	}

	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Published.Before(fresh[j].Published) })
	if len(fresh) > maxAnnouncePerCycle {
		log.Printf("[REDDIT] %d new posts; announcing %d now, rest next cycle", len(fresh), maxAnnouncePerCycle)
		fresh = fresh[:maxAnnouncePerCycle]
	}
	for _, p := range fresh {
		if !f.bot.IsConnected() { // keep the rest unseen for after the reconnect
			break
		}
		if i := config.FindRedditSub(cfg.Reddit.Subreddits, p.Sub); i >= 0 && len(cfg.Reddit.Subreddits[i].Channels) > 0 {
			f.bot.Broadcast(cfg.Reddit.Subreddits[i].Channels, FormatLine(p))
		}
		if err := f.db.MarkSeen(p); err != nil {
			log.Printf("[REDDIT] mark seen: %v", err)
		}
		time.Sleep(f.gap)
	}
	if err := f.db.Prune(); err != nil {
		log.Printf("[REDDIT] prune: %v", err)
	}
}

// unseen filters posts already stored. On the first poll of a sub only the newest
// firstPollAnnounce are returned; the older ones are stored silently.
func (f *Fetcher) unseen(sub string, posts []Post) ([]Post, error) {
	known, err := f.db.HasAny(sub)
	if err != nil {
		return nil, err
	}
	var out []Post
	for _, p := range posts {
		seen, err := f.db.Seen(p.ID)
		if err != nil {
			return nil, err
		}
		if !seen {
			out = append(out, p)
		}
	}
	if known || len(out) <= firstPollAnnounce {
		return out, nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	for _, p := range out[firstPollAnnounce:] {
		if err := f.db.MarkSeen(p); err != nil {
			return nil, err
		}
	}
	return out[:firstPollAnnounce], nil
}

func (f *Fetcher) get(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, f.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	// Reddit throttles generic user agents hard; it asks for a descriptive one.
	req.Header.Set("User-Agent", meta.Name+"/"+meta.Version+" (IRC bot)")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		// Reddit's bot check answers 200 with an HTML page instead of the feed/JSON.
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			resp.Body.Close()
			return nil, fmt.Errorf("%w (bot check page)", errBlocked)
		}
		return resp, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, fmt.Errorf("%w (HTTP %d)", errBlocked, resp.StatusCode)
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, errors.New("subreddit not found")
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
}

// postSubRe takes the subreddit out of a post link (.../r/<sub>/comments/<id>/...).
var postSubRe = regexp.MustCompile(`/r/([A-Za-z0-9_]+)/comments/`)

// fetchFeed reads the newest 100 posts of all subs in one combined listing.
// ponytail: more than 100 posts across all subs within one interval drops the overflow;
// shorten the interval (or split into several requests) if that happens.
func (f *Fetcher) fetchFeed(names []string) ([]Post, error) {
	resp, err := f.get("/r/" + strings.Join(names, "+") + "/new/.rss?limit=100")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	feed, err := gofeed.NewParser().Parse(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	var out []Post
	for _, it := range feed.Items {
		id := strings.TrimPrefix(it.GUID, "t3_")
		if id == "" || it.GUID == id { // not a post (t3_) entry
			continue
		}
		m := postSubRe.FindStringSubmatch(it.Link)
		if m == nil {
			continue
		}
		p := Post{Sub: m[1], ID: it.GUID, Title: it.Title, Link: "https://redd.it/" + id}
		if len(it.Authors) > 0 && it.Authors[0] != nil {
			p.Author = strings.TrimPrefix(strings.TrimPrefix(it.Authors[0].Name, "/"), "u/")
		}
		switch {
		case it.PublishedParsed != nil:
			p.Published = *it.PublishedParsed
		case it.UpdatedParsed != nil:
			p.Published = *it.UpdatedParsed
		default:
			p.Published = time.Now()
		}
		out = append(out, p)
	}
	return out, nil
}

// CheckNSFW reports whether a subreddit is marked over18 (best effort, from about.json).
// ponytail: the .rss feed has no per-post NSFW flag, so NSFW posts inside a SFW sub are not
// filtered; that needs the OAuth JSON API.
func (f *Fetcher) CheckNSFW(name string) (bool, error) {
	resp, err := f.get("/r/" + name + "/about.json")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var about struct {
		Data struct {
			Over18 bool `json:"over18"`
		} `json:"data"`
	}
	if err := httpx.DecodeJSON(resp.Body, maxBody, &about); err != nil {
		return false, err
	}
	return about.Data.Over18, nil
}
