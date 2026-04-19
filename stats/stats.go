package stats

import (
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
	users      map[string]struct{}

	// Broadcaster
	subscribers map[chan StatEntry]bool
	subMu       sync.RWMutex

	enabled bool
	quit    chan struct{}
}

// NewTracker initializes a new statistics tracker.
func NewTracker(cfg *config.Config, db *Database) *Tracker {
	return &Tracker{
		cfg:         cfg,
		db:          db,
		users:       make(map[string]struct{}),
		subscribers: make(map[chan StatEntry]bool),
		enabled:     cfg.Stats.Enabled,
		quit:        make(chan struct{}),
	}
}

// Start begins the snapshot loop.
func (t *Tracker) Start() {
	if !t.enabled {
		return
	}

	interval := time.Duration(t.cfg.Stats.Interval) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("Stats tracker started (Interval: %v)", interval)

	for {
		select {
		case <-ticker.C:
			t.snapshot()
		case <-t.quit:
			return
		}
	}
}

// Stop halts the snapshot loop.
func (t *Tracker) Stop() {
	close(t.quit)
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

func (t *Tracker) snapshot() {
	t.mu.Lock()
	entry := StatEntry{
		Timestamp:  time.Now(),
		Messages:   t.messages,
		Actions:    t.actions,
		AIRequests: t.aiRequests,
		UserCount:  len(t.users),
		Joins:      t.joins,
		Parts:      t.parts,
	}

	// Reset counters for next window
	t.messages = 0
	t.actions = 0
	t.aiRequests = 0
	t.joins = 0
	t.parts = 0
	// Keep users list or clear it? Usually we clear it for "active users in window"
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

func (t *Tracker) IsEnabled() bool {
	t.subMu.RLock()
	defer t.subMu.RUnlock()
	return t.enabled
}

func (t *Tracker) SetEnabled(enabled bool) {
	t.subMu.Lock()
	t.enabled = enabled
	t.subMu.Unlock()
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
