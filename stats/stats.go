package stats

import (
	"context"
	"log"
	"sync"
	"time"

	"botIAask/config"
	"botIAask/internal/guard"
)

// networkCounters holds one window's activity counters for one IRC network.
type networkCounters struct {
	messages, actions, aiRequests, joins, parts, adminCmds, failedAuth int
	users                                                              map[string]struct{}
}

func newNetworkCounters() *networkCounters {
	return &networkCounters{users: make(map[string]struct{})}
}

// Tracker monitors bot activity and handles interval-based snapshots.
type Tracker struct {
	cfg *config.Config
	db  *Database
	mu  sync.Mutex

	// counters is keyed by IRC network name, so activity on one network never inflates or
	// undercounts another's (see LogMessage et al., all of which now take a network
	// parameter). Reset to empty at the start of each snapshot window.
	counters map[string]*networkCounters

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
	runCancel      context.CancelFunc
	lastStatsPrune time.Time // throttles db.Cleanup (stats retention)
}

// NewTracker initializes a new statistics tracker.
func NewTracker(cfg *config.Config, db *Database) *Tracker {
	return &Tracker{
		cfg:             cfg,
		db:              db,
		counters:        make(map[string]*networkCounters),
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

// counterFor returns this window's counters for network, creating them on first use.
// Caller must hold t.mu.
func (t *Tracker) counterFor(network string) *networkCounters {
	c, ok := t.counters[network]
	if !ok {
		c = newNetworkCounters()
		t.counters[network] = c
	}
	return c
}

// LogMessage records a message event on network.
func (t *Tracker) LogMessage(network, sender string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.counterFor(network)
	c.messages++
	c.users[sender] = struct{}{}
}

// LogAction records an IRC action (/me) on network.
func (t *Tracker) LogAction(network, sender string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.counterFor(network)
	c.actions++
	c.users[sender] = struct{}{}
}

// LogAIRequest records an AI request on network.
func (t *Tracker) LogAIRequest(network string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counterFor(network).aiRequests++
}

// LogJoin records a join event on network.
func (t *Tracker) LogJoin(network string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counterFor(network).joins++
}

// LogPart records a part/quit event on network.
func (t *Tracker) LogPart(network string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counterFor(network).parts++
}

// LogAdminCommand records an administrative command execution on network.
func (t *Tracker) LogAdminCommand(network string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counterFor(network).adminCmds++
}

// LogFailedAuth records a failed admin login attempt on network.
func (t *Tracker) LogFailedAuth(network string) {
	if !t.IsEnabled() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counterFor(network).failedAuth++
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

// snapshot emits one StatEntry per configured network (a heartbeat even for a network with
// zero activity this window, matching the pre-multi-network behavior of always broadcasting
// a tick), persists them, then broadcasts a merged aggregate entry FIRST — so a client that
// doesn't know about the network dimension keeps seeing exactly the single-series stream it
// always has — followed by the per-network breakdowns for a network-aware client to filter on.
func (t *Tracker) snapshot() {
	t.subMu.RLock()
	cfg := t.cfg
	t.subMu.RUnlock()

	now := time.Now()

	t.mu.Lock()
	counters := t.counters
	t.counters = make(map[string]*networkCounters)
	t.mu.Unlock()

	networkNames := make([]string, 0, len(cfg.IRC.Networks))
	for _, n := range cfg.IRC.Networks {
		networkNames = append(networkNames, n.Name)
	}
	if len(networkNames) == 0 {
		networkNames = []string{""}
	}

	t.adminMu.RLock()
	entries := make([]StatEntry, 0, len(networkNames))
	for _, network := range networkNames {
		e := StatEntry{Timestamp: now, Network: network}
		if c := counters[network]; c != nil {
			e.Messages, e.Actions, e.AIRequests = c.messages, c.actions, c.aiRequests
			e.Joins, e.Parts = c.joins, c.parts
			e.UserCount = len(c.users)
			e.AdminCommands = c.adminCmds
			e.FailedAuths = c.failedAuth
		}
		if nicks := t.adminNicksByNet[network]; len(nicks) > 0 {
			e.AdminNicknames = append([]string(nil), nicks...)
		}
		e.LoggedInAdmins = len(e.AdminNicknames)
		if cm := t.chanAdminsByNet[network]; len(cm) > 0 {
			chans := make(map[string][]string, len(cm))
			for ch, admins := range cm {
				chans[ch] = admins
			}
			e.ChannelAdmins = chans
		}
		entries = append(entries, e)
	}
	t.adminMu.RUnlock()

	if t.db != nil {
		for _, e := range entries {
			if cfg.Stats.ShouldSaveToDB() {
				if err := t.db.SaveEntry(e); err != nil {
					log.Printf("Error saving stats: %v", err)
				}
			}
		}
		t.maybePruneStatsHistory(cfg)
	}

	t.broadcast(mergeStatEntries(now, entries))
	for _, e := range entries {
		t.broadcast(e)
	}
}

// mergeStatEntries sums per-network entries into one aggregate row (Network == ""), for
// clients that don't filter by network — the default the dashboard's activity chart shows.
// Admin nicknames are deduplicated and channel presence re-keyed "<network>:<channel>",
// matching the merged shape adminSnapshotLocked has always produced.
func mergeStatEntries(ts time.Time, entries []StatEntry) StatEntry {
	merged := StatEntry{Timestamp: ts, ChannelAdmins: map[string][]string{}}
	seenNick := make(map[string]bool)
	for _, e := range entries {
		merged.Messages += e.Messages
		merged.Actions += e.Actions
		merged.AIRequests += e.AIRequests
		merged.UserCount += e.UserCount
		merged.Joins += e.Joins
		merged.Parts += e.Parts
		merged.AdminCommands += e.AdminCommands
		merged.FailedAuths += e.FailedAuths
		for _, n := range e.AdminNicknames {
			if !seenNick[n] {
				seenNick[n] = true
				merged.AdminNicknames = append(merged.AdminNicknames, n)
			}
		}
		for ch, admins := range e.ChannelAdmins {
			merged.ChannelAdmins[config.JoinNetworkChannel(e.Network, ch)] = admins
		}
	}
	merged.LoggedInAdmins = len(merged.AdminNicknames)
	if len(merged.ChannelAdmins) == 0 {
		merged.ChannelAdmins = nil
	}
	return merged
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

// GetHistory retrieves historical stats from the database. network == "" returns the
// aggregate view (summed across every network sharing a timestamp); pass a network name
// for just its history.
func (t *Tracker) GetHistory(since time.Time, network string) ([]StatEntry, error) {
	if t.db == nil {
		return []StatEntry{}, nil
	}
	return t.db.GetStatsSince(since, network)
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
