package github

import (
	"context"
	"fmt"
	"log"
	"sort"
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
var announcePace = 2 * time.Second // var: tests zero it

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

	fetchMu sync.Mutex // one Fetch at a time: a restarted loop must not re-announce an in-flight pass

	hooks    chan webhookJob // webhook deliveries, announced in arrival order by one worker
	hookOnce sync.Once
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
		hooks:      make(chan webhookJob, 64),
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

	if intervalMin <= 0 {
		intervalMin = 10 // NewTicker panics on <=0; mirrors applyGitHubTrackerDefaults
	}
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

	if !f.fetchMu.TryLock() {
		return
	}
	defer f.fetchMu.Unlock()

	f.mu.Lock()
	cfg := f.cfg
	f.mu.Unlock()

	f.lfMu.Lock()
	f.lastFetch = time.Now()
	f.lfMu.Unlock()

	for _, r := range cfg.GitHubTracker.Repos {
		if r.Webhook {
			continue // delivered by POST /api/github/webhook; polling it too would announce twice
		}
		if f.pollRepo(r) {
			break
		}
	}

	if err := f.db.CleanupOlderThan(seenEventRetentionDays); err != nil {
		log.Printf("[GITHUB] Cleanup error: %v", err)
	}
}

// pollRepo fetches and announces one repo under its own 60s deadline (a shared deadline would
// starve repos late in the list when announcements are paced). It reports whether the caller
// should stop working through the repo list this cycle.
func (f *Fetcher) pollRepo(r config.GitHubTrackerRepoConfig) (stop bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repoKey := r.FullName()

	etag, err := f.db.GetETag(repoKey)
	if err != nil {
		log.Printf("[GITHUB] Failed to read ETag for %s: %v", repoKey, err)
	}
	token, err := f.cryptor.Decrypt(r.TokenEncrypted)
	if err != nil {
		log.Printf("[GITHUB] Failed to decrypt token for %s: %v", repoKey, err)
		f.setRepoStatus(repoKey, RepoStatus{OK: false, Error: "token decrypt failed"})
		return false
	}

	// Commits come from branch heads (real time, one line per commit); the Events API only
	// lags them by minutes to hours, so it is used for everything else.
	var anns []Announcement
	if allowedEventTypeSet(r.EventTypes)("PushEvent") {
		pushes, rate, err := f.pollBranches(ctx, r, token)
		if err != nil {
			log.Printf("[GITHUB] Failed to poll branches for %s: %v", repoKey, err)
			f.setRepoStatus(repoKey, RepoStatus{OK: false, Error: err.Error(), RateRemaining: rate})
			return false
		}
		anns = pushes
	}

	result, err := FetchRepoEvents(ctx, r.Owner, r.Repo, token, etag)
	if err != nil {
		log.Printf("[GITHUB] Failed to fetch events for %s: %v", repoKey, err)
		rateRemaining := -1
		if result != nil {
			rateRemaining = result.RateRemaining
		}
		f.setRepoStatus(repoKey, RepoStatus{OK: false, Error: err.Error(), RateRemaining: rateRemaining})
		return false
	}

	if result.ETag != "" && result.ETag != etag {
		if err := f.db.SetETag(repoKey, result.ETag); err != nil {
			log.Printf("[GITHUB] Failed to store ETag for %s: %v", repoKey, err)
		}
	}
	if !result.NotModified {
		anns = append(anns, eventAnnouncements(r, result.Events)...)
	}

	// No stored ETag = the repo's first poll: record its history without printing it.
	f.announce(r, anns, etag == "")
	f.setRepoStatus(repoKey, RepoStatus{OK: true, RateRemaining: result.RateRemaining})

	// The poll interval is the backoff: stop working through the rest of the repo
	// list this cycle if the budget is nearly spent, and let the next tick resume.
	if result.RateRemaining >= 0 && result.RateRemaining <= 1 {
		log.Printf("[GITHUB] Rate limit nearly exhausted (%d remaining), pausing further repos until next cycle", result.RateRemaining)
		return true
	}
	return false
}

// pollBranches turns branch moves into one announcement per new commit. While nothing moved
// it costs one conditional request (a 304 is free against the rate limit); each moved
// branch costs one more. The branch list's ETag is stored only once every moved branch was
// handled, so a failed fetch is retried instead of being hidden behind a 304.
// ponytail: a branch seen for the first time is only remembered (its create event announces
// it) and the first poll of a repo only records heads, so the commits of a new branch's first
// push are not listed.
func (f *Fetcher) pollBranches(ctx context.Context, r config.GitHubTrackerRepoConfig, token string) (anns []Announcement, rate int, err error) {
	repoKey := r.FullName()
	etagKey := repoKey + "/branches"
	etag, _ := f.db.GetETag(etagKey)
	res, branches, err := FetchBranches(ctx, r.Owner, r.Repo, token, etag)
	rate = -1
	if res != nil {
		rate = res.RateRemaining
	}
	if err != nil || res.NotModified {
		return nil, rate, err
	}
	heads, err := f.db.BranchHeads(repoKey)
	if err != nil {
		return nil, rate, err
	}

	complete := true
	live := make(map[string]bool, len(branches))
	for _, b := range branches {
		live[b.Name] = true
		old, known := heads[b.Name]
		if old == b.Commit.SHA {
			continue
		}
		if known {
			commits, err := FetchCommits(ctx, r.Owner, r.Repo, token, b.Commit.SHA, 20)
			if err != nil {
				log.Printf("[GITHUB] Failed to fetch commits of %s:%s: %v", repoKey, b.Name, err)
				complete = false
				continue
			}
			anns = append(anns, branchAnnouncements(repoKey, b.Name, old, commits)...)
		}
		if err := f.db.SetBranchHead(repoKey, b.Name, b.Commit.SHA); err != nil {
			log.Printf("[GITHUB] Failed to store head of %s:%s: %v", repoKey, b.Name, err)
			complete = false
		}
	}
	for name := range heads {
		if !live[name] {
			_ = f.db.DeleteBranchHead(repoKey, name)
		}
	}
	if complete && res.ETag != "" {
		if err := f.db.SetETag(etagKey, res.ETag); err != nil {
			log.Printf("[GITHUB] Failed to store branch ETag for %s: %v", repoKey, err)
		}
	}
	return anns, rate, nil
}

// DB exposes the fetcher's database for "!gh search" (irc/ has no other path to it —
// Fetcher.db is otherwise private).
func (f *Fetcher) DB() *Database { return f.db }

// eventAnnouncements turns raw events into announcements, oldest first as GitHub sends
// them newest first.
func eventAnnouncements(r config.GitHubTrackerRepoConfig, events []RawEvent) []Announcement {
	meta := RepoMeta{CachedDescription: r.CachedDescription}
	allowed := allowedEventTypeSet(r.EventTypes)
	var out []Announcement
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if !allowed(ev.Type) {
			continue
		}
		if ev.Type == "PushEvent" { // webhook deliveries carry commits; polled pushes carry none
			out = append(out, webhookPush(ev)...)
			continue
		}
		if ann, ok := ExtractAnnouncement(ev, meta); ok {
			ann.ID, ann.At = ev.ID, ev.CreatedAt
			out = append(out, ann)
		}
	}
	return out
}

// maxLines caps what one repo prints per cycle; the rest is summed up in one line.
const maxLines = 5

// announce prints what is new, oldest first. Everything new is recorded (so a commit seen on
// one branch is not repeated when another branch merges it) but at most maxLines are printed.
// silent records without printing.
func (f *Fetcher) announce(r config.GitHubTrackerRepoConfig, anns []Announcement, silent bool) {
	sort.SliceStable(anns, func(i, j int) bool { return anns[i].At.Before(anns[j].At) })
	repoKey := r.FullName()
	var fresh []Announcement
	for _, ann := range anns {
		key := EventKey(r.Owner, r.Repo, ann.ID)
		seen, err := f.db.EventSeen(key)
		if err != nil {
			log.Printf("[GITHUB] DB error checking seen state for %s: %v", key, err)
			continue
		}
		if seen {
			continue
		}
		// Mark seen BEFORE broadcasting so a failed broadcast doesn't cause a retry storm
		// (same ordering as rss/fetcher.go's MarkSeen-before-Broadcast).
		if err := f.db.MarkEventSeen(key, repoKey, ann.Kind, ann.RefID, ann.Message, ann.At); err != nil {
			log.Printf("[GITHUB] Failed to mark event seen for %s: %v", key, err)
			continue
		}
		fresh = append(fresh, ann)
	}
	if silent || len(fresh) == 0 {
		return
	}

	// Shortening happens here, after the cap, so a burst costs at most maxLines shortener
	// calls (a network call to a third-party service with its own fallback chain).
	f.mu.Lock()
	shortener := f.cfg.RSS.URLShortener
	f.mu.Unlock()
	if extra := len(fresh) - maxLines; extra > 0 {
		f.bot.Broadcast(r.Channels, colorize("14", "("+repoKey+")")+fmt.Sprintf(" +%d earlier updates not shown", extra))
		fresh = fresh[extra:]
	}
	for i, ann := range fresh {
		if i > 0 {
			time.Sleep(announcePace)
		}
		f.bot.Broadcast(r.Channels, shortenAnnouncementLink(ann, shortener))
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
