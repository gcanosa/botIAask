package stats

import (
	"path/filepath"
	"testing"
	"time"

	"botIAask/config"
)

func twoNetworkCfg() *config.Config {
	return &config.Config{
		Stats: config.StatsConfig{Enabled: true, Interval: 60},
		IRC: config.IRCConfig{Networks: []config.IRCNetworkConfig{
			{Name: "alpha"}, {Name: "beta"},
		}},
	}
}

// TestPerNetworkCounters_Isolated guards the stats-collision bug: activity on one network
// must not inflate another's counters, and the same nick active on two networks must count
// as two distinct users rather than collapsing into one.
func TestPerNetworkCounters_Isolated(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	sdb, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer sdb.Close()

	tr := NewTracker(twoNetworkCfg(), sdb)
	tr.LogMessage("alpha", "bob")
	tr.LogMessage("alpha", "bob")
	tr.LogMessage("beta", "bob") // same nick, different network
	tr.LogJoin("alpha")

	tr.snapshot()

	alphaHist, err := sdb.GetStatsSince(time.Time{}, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(alphaHist) != 1 || alphaHist[0].Messages != 2 || alphaHist[0].UserCount != 1 || alphaHist[0].Joins != 1 {
		t.Fatalf("alpha row wrong: %+v", alphaHist)
	}

	betaHist, err := sdb.GetStatsSince(time.Time{}, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(betaHist) != 1 || betaHist[0].Messages != 1 || betaHist[0].UserCount != 1 || betaHist[0].Joins != 0 {
		t.Fatalf("beta row wrong: %+v", betaHist)
	}

	// Aggregate (no network filter) sums both networks' rows for the shared timestamp.
	agg, err := sdb.GetStatsSince(time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(agg) != 1 || agg[0].Messages != 3 || agg[0].UserCount != 2 {
		t.Fatalf("aggregate row wrong: %+v", agg)
	}
}

// TestSnapshot_HeartbeatEveryConfiguredNetwork guards a regression where a quiet network
// (zero activity this window) would be skipped entirely instead of still emitting a
// zero-valued row, which the old single-row-per-tick model always did.
func TestSnapshot_HeartbeatEveryConfiguredNetwork(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	sdb, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()

	tr := NewTracker(twoNetworkCfg(), sdb)
	tr.LogMessage("alpha", "bob") // beta stays quiet
	tr.snapshot()

	betaHist, err := sdb.GetStatsSince(time.Time{}, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(betaHist) != 1 {
		t.Fatalf("expected a heartbeat row for the quiet network beta, got %d rows", len(betaHist))
	}
	if betaHist[0].Messages != 0 {
		t.Fatalf("quiet network row should be zero-valued, got %+v", betaHist[0])
	}
}

// TestSnapshot_BroadcastsMergedFirst guards backward compatibility for a client that
// doesn't filter by network (Network == ""): it must see exactly one row per tick with the
// pre-multi-network aggregate shape, before any per-network breakdown rows.
func TestSnapshot_BroadcastsMergedFirst(t *testing.T) {
	tr := NewTracker(twoNetworkCfg(), nil)
	ch := tr.Subscribe()
	defer tr.Unsubscribe(ch)

	tr.LogMessage("alpha", "bob")
	tr.LogMessage("beta", "carol")
	tr.snapshot()

	first := <-ch
	if first.Network != "" {
		t.Fatalf("first broadcast entry must be the merged aggregate (Network==\"\"), got %q", first.Network)
	}
	if first.Messages != 2 {
		t.Fatalf("merged entry should sum both networks: got %d messages", first.Messages)
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		e := <-ch
		seen[e.Network] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("expected per-network breakdown entries for alpha and beta, got %+v", seen)
	}
}

func TestGetAdmins_DedupAcrossNetworks(t *testing.T) {
	tr := NewTracker(twoNetworkCfg(), nil)
	tr.UpdateAdminData("alpha", []string{"root", "sam"}, map[string][]string{"#chan": {"root"}})
	tr.UpdateAdminData("beta", []string{"root"}, map[string][]string{"#chan": {"root"}})

	nicks, chans := tr.GetAdmins()
	if len(nicks) != 2 {
		t.Fatalf("expected root deduped across networks, got %v", nicks)
	}
	if _, ok := chans["alpha:#chan"]; !ok {
		t.Fatalf("expected alpha:#chan key, got %v", chans)
	}
	if _, ok := chans["beta:#chan"]; !ok {
		t.Fatalf("expected beta:#chan key, got %v", chans)
	}
}
