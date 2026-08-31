package stats

import (
	"context"
	"log"
	"sync"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
)

// Tracker monitors bot activity and handles interval-based snapshots.
type Tracker struct {
	cfg   *config.Config
	db    *Database
	mu    sync.Mutex
	state StatEntry

	// Current window stats
	messages   int
	actions    int
	aiRequests int
	joins      int
	parts      int
	adminCmds  int
	failedAuth int
	users      map[string]struct{}

	// Admin Nicknames & Presence, keyed by IRC network so a multi-network bot doesn't
	// have one network's JOIN/PART/NICK event overwrite another's (see GetAdmins).
	adminNicksByNet map[string][]string
	chanAdminsByNet map[string]map[string][]string
	adminMu         sync.RWMutex

	// Broadcaster
	subscribers map[chan StatEntry]bool
	subMu       sync.RWMutex

	enabled bool
	loopMu  sync.Mutex
	runWG   sync.WaitGroup
	// runCancel stops the active snapshot loop (restarted on ApplyConfig / SetEnabled / Start).
	runCancel        context.CancelFunc
	lastStatsPrune   time.Time // throttles db.Cleanup (stats retention)
}

// NewTracker initializes a new statistics tracker.
func NewTracker(cfg *config.Config, db *Database) *Tracker {
	return &Tracker{
		cfg:             cfg,
		db:              db,
		users:           make(map[string]struct{}),
		subscribers:     make(map[chan StatEntry]bool),
		enabled:         cfg.Stats.Enabled,
		adminNicksByNet: make(map[string][]string),
		chanAdminsByNet: make(map[string]map[string][]string),
	}
}

// Start begins the snapshot loop when stats are enabled.
func (t *Tracker) Start() {
	t.subMu.Lock()
	t.enabled = t.cfg.Stats.Enabled
	t.subMu.Unlock()
	t.restartTrackingLoop()
}

func (t *Tracker) restartTrackingLoop() {
	t.loopMu.Lock()
	if t.runCancel != nil {
		t.runCancel()
		t.runCancel = nil
	}
	t.loopMu.Unlock()
	t.runWG.Wait()

	if !t.IsEnabled() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.loopMu.Lock()
	t.runCancel = cancel
	t.loopMu.Unlock()
	t.runWG.Add(1)
	guard.Go("stats tracker loop", func() {
		defer t.runWG.Done()
		t.runLoop(ctx)
	})
}

func (t *Tracker) runLoop(ctx context.Context) {
	for {
		if !t.IsEnabled() {
			return
		}
		t.subMu.RLock()
		interval := time.Duration(t.cfg.Stats.Interval) * time.Second
		t.subMu.RUnlock()
		if interval <= 0 {
			interval = 60 * time.Second
		}
		ticker := time.NewTicker(interval)
		select {
		case <-ticker.C:
			ticker.Stop()
			if !t.IsEnabled() {
				return
			}
			t.snapshot()
		case <-ctx.Done():
			ticker.Stop()
			return
		}
	}
}

// ApplyConfig replaces config and restarts the snapshot loop to pick up interval / enabled flags.
func (t *Tracker) ApplyConfig(cfg *config.Config) {
	t.subMu.Lock()
	t.cfg = cfg
	t.enabled = cfg.Stats.Enabled
	t.subMu.Unlock()
	t.restartTrackingLoop()
}

// LogMessage records a message event.
func (t *Tracker) LogMessage(sender string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messages++
	t.users[sender] = struct{}{}
}

// LogAction records an IRC action (/me).
func (t *Tracker) LogAction(sender string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.actions++
	t.users[sender] = struct{}{}
}

// LogAIRequest records an AI request.
func (t *Tracker) LogAIRequest() {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.aiRequests++
}

// LogJoin records a join event.
func (t *Tracker) LogJoin() {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.joins++
}

// LogPart records a part/quit event.
func (t *Tracker) LogPart() {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts++
}

// LogAdminCommand records an administrative command execution.
func (t *Tracker) LogAdminCommand() {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.adminCmds++
}

// LogFailedAuth records a failed admin login attempt.
func (t *Tracker) LogFailedAuth() {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failedAuth++
}

// UpdateAdminData updates the logged-in admin nicknames and channel presence for one
// IRC network, leaving other networks' data untouched.
func (t *Tracker) UpdateAdminData(network string, nicknames []string, channelAdmins map[string][]string) {
	t.adminMu.Lock()
	defer t.adminMu.Unlock()
	t.adminNicksByNet[network] = nicknames
	t.chanAdminsByNet[network] = channelAdmins
}

// GetAdmins returns the logged-in admins and channel presence merged across all
// networks: nicknames deduplicated, channel presence keyed "<network>:<channel>"
// (config.JoinNetworkChannel) so the same channel name on two networks doesn't collide.
func (t *Tracker) GetAdmins() ([]string, map[string][]string) {
	t.adminMu.RLock()
	defer t.adminMu.RUnlock()
	return t.adminSnapshotLocked()
}

// adminSnapshotLocked merges the per-network admin state. Caller must hold adminMu.
func (t *Tracker) adminSnapshotLocked() ([]string, map[string][]string) {
	seen := make(map[string]bool)
	var nicks []string
	for _, list := range t.adminNicksByNet {
		for _, n := range list {
			if !seen[n] {
				seen[n] = true
				nicks = append(nicks, n)
			}
		}
	}
	chans := make(map[string][]string)
	for network, cm := range t.chanAdminsByNet {
		for ch, admins := range cm {
			chans[config.JoinNetworkChannel(network, ch)] = admins
		}
	}
	return nicks, chans
}

func (t *Tracker) snapshot() {
	t.mu.Lock()
	entry := StatEntry{
		Timestamp:  time.Now(),
		Messages:   t.messages,
		Actions:    t.actions,
		AIRequests: t.aiRequests,
		Joins:      t.joins,
		Parts:      t.parts,
		UserCount:  len(t.users),
	}

	// Get current admins for real-time broadcast
	t.adminMu.RLock()
	entry.AdminNicknames, entry.ChannelAdmins = t.adminSnapshotLocked()
	entry.AdminCommands = t.adminCmds
	entry.LoggedInAdmins = len(entry.AdminNicknames)
	entry.FailedAuths = t.failedAuth
	t.adminMu.RUnlock()

	// Reset counters for next window
	t.messages = 0
	t.actions = 0
	t.aiRequests = 0
	t.joins = 0
	t.parts = 0
	t.adminCmds = 0
	t.failedAuth = 0
	t.users = make(map[string]struct{})
	t.mu.Unlock()

	t.subMu.RLock()
	cfg := t.cfg
	t.subMu.RUnlock()

	// Save to DB if enabled
	if cfg.Stats.ShouldSaveToDB() && t.db != nil {
		if err := t.db.SaveEntry(entry); err != nil {
			log.Printf("Error saving stats: %v", err)
		}
	}
	if t.db != nil {
		t.maybePruneStatsHistory(cfg)
	}

	// Broadcast to subscribers
	t.broadcast(entry)
}

func (t *Tracker) maybePruneStatsHistory(cfg *config.Config) {
	if t.db == nil || cfg.Stats.RetentionDays <= 0 {
		return
	}
	if !t.lastStatsPrune.IsZero() && time.Since(t.lastStatsPrune) < 24*time.Hour {
		return
	}
	t.lastStatsPrune = time.Now()
	if err := t.db.Cleanup(cfg.Stats.RetentionDays); err != nil {
		log.Printf("stats retention cleanup: %v", err)
	}
}

// GetHistory retrieves historical stats from the database.
func (t *Tracker) GetHistory(since time.Time) ([]StatEntry, error) {
	if t.db == nil {
		return []StatEntry{}, nil
	}
	return t.db.GetStatsSince(since)
}

func (t *Tracker) IsEnabled() bool {
	t.subMu.RLock()
	defer t.subMu.RUnlock()
	return t.enabled
}

func (t *Tracker) SetEnabled(enabled bool) {
	t.subMu.Lock()
	t.enabled = enabled
	t.subMu.Unlock()
	t.restartTrackingLoop()
}

// Subscribe returns a channel that receives real-time stat snapshots.
func (t *Tracker) Subscribe() chan StatEntry {
	ch := make(chan StatEntry, 10)
	t.subMu.Lock()
	t.subscribers[ch] = true
	t.subMu.Unlock()
	return ch
}

// Unsubscribe removes a channel from the broadcaster.
func (t *Tracker) Unsubscribe(ch chan StatEntry) {
	t.subMu.Lock()
	delete(t.subscribers, ch)
	t.subMu.Unlock()
	close(ch)
}

func (t *Tracker) broadcast(entry StatEntry) {
	t.subMu.RLock()
	defer t.subMu.RUnlock()
	for ch := range t.subscribers {
		select {
		case ch <- entry:
		default:
			// Buffer full, skip this subscriber for now
		}
	}
}
