package github

import (
	"context"
	"log"
	"sync"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
)

// BotInterface is the minimal surface the fetcher needs to announce to IRC. *irc.Bot
// satisfies this already (see rss.BotInterface for the identical pattern).
type BotInterface interface {
	Broadcast(channels []string, message string)
	IsConnected() bool
}

// RepoStatus is the last poll result for one tracked repo (admin UI).
type RepoStatus struct {
	Repo          string     `json:"repo"`
	OK            bool       `json:"ok"`
	Error         string     `json:"error,omitempty"`
	LastPolledAt  *time.Time `json:"last_polled_at,omitempty"`
	RateRemaining int        `json:"rate_remaining,omitempty"`
}

// seenEventRetentionDays mirrors GitHub's own Events API visibility window, so seen_events
// never grows past what the API itself could ever re-serve.
const seenEventRetentionDays = 90

// announcePace is the delay between consecutive IRC broadcasts within one Fetch() cycle,
// avoiding a flood when a repo has queued up many events since the last successful poll.
const announcePace = 2 * time.Second

type Fetcher struct {
	cfg     *config.Config
	bot     BotInterface
	db      *Database
	cryptor *Cryptor

	mu       sync.Mutex
	enabled  bool
	stopChan chan struct{}

	lastFetch time.Time
	lfMu      sync.RWMutex

	repoStatus   map[string]RepoStatus
	repoStatusMu sync.RWMutex
}

func NewFetcher(cfg *config.Config, bot BotInterface, db *Database, cryptor *Cryptor) *Fetcher {
	return &Fetcher{
		cfg:        cfg,
		bot:        bot,
		db:         db,
		cryptor:    cryptor,
		enabled:    cfg.GitHubTracker.Enabled,
		stopChan:   make(chan struct{}),
		repoStatus: make(map[string]RepoStatus),
	}
}

func (f *Fetcher) Start() {
	f.mu.Lock()
	if !f.enabled {
		f.mu.Unlock()
		return
	}
	// Capture the stop channel once: Stop()/SetEnabled(false) close it and install a
	// fresh one, so re-reading f.stopChan later in this loop could observe the new (open)
	// channel and miss the close entirely (same race avoided in rss/fetcher.go).
	stop := f.stopChan
	intervalMin := f.cfg.GitHubTracker.IntervalMinutes
	f.mu.Unlock()

	ticker := time.NewTicker(time.Duration(intervalMin) * time.Minute)
	defer ticker.Stop()

	for i := 0; i < 24; i++ {
		if f.bot.IsConnected() {
			break
		}
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

func (f *Fetcher) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enabled {
		close(f.stopChan)
		f.enabled = false
		f.stopChan = make(chan struct{})
	}
}

func (f *Fetcher) SetEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.enabled == enabled {
		return
	}

	if enabled {
		f.enabled = true
		guard.Go("github tracker", f.Start)
	} else {
		close(f.stopChan)
		f.enabled = false
		f.stopChan = make(chan struct{})
	}
}

func (f *Fetcher) IsEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

// SetConfig atomically replaces the live config without restarting the poll loop. Use this
// when only non-structural settings change (e.g. repo list, event-type filters, channels).
func (f *Fetcher) SetConfig(cfg *config.Config) {
	f.mu.Lock()
	f.cfg = cfg
	f.mu.Unlock()
}

// ApplyConfig swaps in a new root config and restarts the poll loop only when Enabled or
// the ticker interval actually changed, so a rehash touching unrelated settings (repo
// list, channels) doesn't tear down and relaunch the running loop.
func (f *Fetcher) ApplyConfig(cfg *config.Config) {
	f.mu.Lock()
	sameLoop := f.enabled == cfg.GitHubTracker.Enabled && f.cfg.GitHubTracker.IntervalMinutes == cfg.GitHubTracker.IntervalMinutes
	f.cfg = cfg
	f.mu.Unlock()

	if sameLoop {
		return
	}
	if f.IsEnabled() {
		f.Stop()
	}
	if cfg.GitHubTracker.Enabled {
		f.SetEnabled(true)
	}
}

func (f *Fetcher) GetLastFetchTime() time.Time {
	f.lfMu.RLock()
	defer f.lfMu.RUnlock()
	return f.lastFetch
}

// EncryptToken/DecryptToken expose the fetcher's cryptor so callers (the web handlers)
// don't need a separate Cryptor reference threaded through NewServer.
func (f *Fetcher) EncryptToken(plain string) (string, error) { return f.cryptor.Encrypt(plain) }
func (f *Fetcher) DecryptToken(enc string) (string, error)   { return f.cryptor.Decrypt(enc) }

// RepoStatuses returns one row per configured repo, in order, for the admin UI.
func (f *Fetcher) RepoStatuses() []RepoStatus {
	f.mu.Lock()
	repos := f.cfg.GitHubTracker.Repos
	f.mu.Unlock()

	f.repoStatusMu.RLock()
	defer f.repoStatusMu.RUnlock()
	out := make([]RepoStatus, 0, len(repos))
	for _, r := range repos {
		key := r.FullName()
		if st, ok := f.repoStatus[key]; ok {
			out = append(out, st)
			continue
		}
		out = append(out, RepoStatus{Repo: key, OK: false, Error: "not yet polled"})
	}
	return out
}

func (f *Fetcher) setRepoStatus(repo string, st RepoStatus) {
	now := time.Now()
	st.Repo = repo
	st.LastPolledAt = &now
	f.repoStatusMu.Lock()
	f.repoStatus[repo] = st
	f.repoStatusMu.Unlock()
}

func (f *Fetcher) Fetch() {
	if !f.bot.IsConnected() {
		return
	}

	f.mu.Lock()
	cfg := f.cfg
	f.mu.Unlock()

	f.lfMu.Lock()
	f.lastFetch = time.Now()
	f.lfMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, r := range cfg.GitHubTracker.Repos {
		repoKey := r.FullName()

		etag, err := f.db.GetETag(repoKey)
		if err != nil {
			log.Printf("[GITHUB] Failed to read ETag for %s: %v", repoKey, err)
		}
		token, err := f.cryptor.Decrypt(r.TokenEncrypted)
		if err != nil {
			log.Printf("[GITHUB] Failed to decrypt token for %s: %v", repoKey, err)
			f.setRepoStatus(repoKey, RepoStatus{OK: false, Error: "token decrypt failed"})
			continue
		}

		result, err := FetchRepoEvents(ctx, r.Owner, r.Repo, token, etag)
		if err != nil {
			log.Printf("[GITHUB] Failed to fetch events for %s: %v", repoKey, err)
			rateRemaining := -1
			if result != nil {
				rateRemaining = result.RateRemaining
			}
			f.setRepoStatus(repoKey, RepoStatus{OK: false, Error: err.Error(), RateRemaining: rateRemaining})
			continue
		}

		if result.ETag != "" && result.ETag != etag {
			if err := f.db.SetETag(repoKey, result.ETag); err != nil {
				log.Printf("[GITHUB] Failed to store ETag for %s: %v", repoKey, err)
			}
		}

		if result.NotModified {
			f.setRepoStatus(repoKey, RepoStatus{OK: true, RateRemaining: result.RateRemaining})
			continue
		}

		f.announceNewEvents(r, result.Events)
		f.setRepoStatus(repoKey, RepoStatus{OK: true, RateRemaining: result.RateRemaining})

		// The poll interval is the backoff: stop working through the rest of the repo
		// list this cycle if the budget is nearly spent, and let the next tick resume.
		if result.RateRemaining >= 0 && result.RateRemaining <= 1 {
			log.Printf("[GITHUB] Rate limit nearly exhausted (%d remaining), pausing further repos until next cycle", result.RateRemaining)
			break
		}
	}

	if err := f.db.CleanupOlderThan(seenEventRetentionDays); err != nil {
		log.Printf("[GITHUB] Cleanup error: %v", err)
	}
}

// DB exposes the fetcher's database for "!gh search" (irc/ has no other path to it —
// Fetcher.db is otherwise private).
func (f *Fetcher) DB() *Database { return f.db }

func (f *Fetcher) announceNewEvents(r config.GitHubTrackerRepoConfig, events []RawEvent) {
	repoKey := r.FullName()
	meta := RepoMeta{CachedDescription: r.CachedDescription}
	eventTypeAllowed := allowedEventTypeSet(r.EventTypes)

	// GitHub returns newest-first; walk oldest-first so seen-marking and the collapsed
	// summaries below both read chronologically (same reversal rss/fetcher.go does for
	// feed entries).
	var toAnnounce []Announcement
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if !eventTypeAllowed(ev.Type) {
			continue
		}
		key := EventKey(r.Owner, r.Repo, ev.ID)
		seen, err := f.db.EventSeen(key)
		if err != nil {
			log.Printf("[GITHUB] DB error checking seen state for %s: %v", key, err)
			continue
		}
		if seen {
			continue
		}
		ann, ok := ExtractAnnouncement(ev, meta)
		if !ok {
			continue
		}

		// Mark seen BEFORE broadcasting so a failed broadcast doesn't cause a retry storm
		// (same ordering as rss/fetcher.go's MarkSeen-before-Broadcast). Store the
		// pre-collapse RefID/Message (not the collapsed/suffixed broadcast text) so
		// "!gh search" sees one row per real event even though broadcast collapses bursts.
		if err := f.db.MarkEventSeen(key, repoKey, ann.Kind, ann.RefID, ann.Message, ev.CreatedAt); err != nil {
			log.Printf("[GITHUB] Failed to mark event seen for %s: %v", key, err)
			continue
		}

		toAnnounce = append(toAnnounce, ann)
	}

	// Collapse to at most one line per event kind so a burst of activity (several pushes
	// in a session, or a first-time catch-up) doesn't flood the channel one line per event.
	// Shortening happens here, AFTER collapsing (not inside ExtractAnnouncement), so a
	// burst of many raw events costs at most 3 shortener calls per repo per cycle instead
	// of one per raw event — the URL shortener is a network call to a third-party service
	// with its own multi-service fallback chain, unbounded by Fetch()'s own context.
	f.mu.Lock()
	shortener := f.cfg.RSS.URLShortener
	f.mu.Unlock()
	for _, ann := range CollapseForBroadcast(toAnnounce) {
		f.bot.Broadcast(r.Channels, shortenAnnouncementLink(ann, shortener))
		time.Sleep(announcePace)
	}
}

func allowedEventTypeSet(filter []string) func(rawType string) bool {
	if len(filter) == 0 {
		return func(string) bool { return true }
	}
	want := make(map[string]bool, len(filter))
	for _, et := range filter {
		switch et {
		case "push":
			want["PushEvent"] = true
		case "pull_request":
			want["PullRequestEvent"] = true
		case "release":
			want["ReleaseEvent"] = true
		case "issues":
			want["IssuesEvent"] = true
		case "create":
			want["CreateEvent"] = true
		case "delete":
			want["DeleteEvent"] = true
		}
	}
	return func(rawType string) bool { return want[rawType] }
}
