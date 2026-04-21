package stats

import (
	"context"
	"log"
	"sync"
	"time"

	"botIAask/config"
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

	// Global Admin Nicknames & Presence
	adminNicks []string
	chanAdmins map[string][]string
	adminMu    sync.RWMutex

	// Broadcaster
	subscribers map[chan StatEntry]bool
	subMu       sync.RWMutex

	enabled bool
	loopMu  sync.Mutex
	runWG   sync.WaitGroup
	// runCancel stops the active snapshot loop (restarted on ApplyConfig / SetEnabled / Start).
	runCancel context.CancelFunc
}

// NewTracker initializes a new statistics tracker.
func NewTracker(cfg *config.Config, db *Database) *Tracker {
	return &Tracker{
		cfg:         cfg,
		db:          db,
		users:       make(map[string]struct{}),
		subscribers: make(map[chan StatEntry]bool),
		enabled:     cfg.Stats.Enabled,
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
	go func() {
		defer t.runWG.Done()
		t.runLoop(ctx)
	}()
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
	t.cfg = cfg
	t.subMu.Lock()
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

// UpdateAdminData updates the list of currently logged-in admin nicknames and their channel presence.
func (t *Tracker) UpdateAdminData(nicknames []string, channelAdmins map[string][]string) {
	t.adminMu.Lock()
	defer t.adminMu.Unlock()
	t.adminNicks = nicknames
	t.chanAdmins = channelAdmins
}

// GetAdmins returns the current logged-in admins and their channel presence.
func (t *Tracker) GetAdmins() ([]string, map[string][]string) {
	t.adminMu.RLock()
	defer t.adminMu.RUnlock()
	return t.adminNicks, t.chanAdmins
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
	entry.AdminNicknames = t.adminNicks
	entry.ChannelAdmins = t.chanAdmins
	entry.AdminCommands = t.adminCmds
	entry.LoggedInAdmins = len(t.adminNicks)
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

	// Save to DB if enabled
	if t.cfg.Stats.SaveToDB && t.db != nil {
		if err := t.db.SaveEntry(entry); err != nil {
			log.Printf("Error saving stats: %v", err)
		}
	}

	// Broadcast to subscribers
	t.broadcast(entry)
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
