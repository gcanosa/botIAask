package stats

import (
	"path/filepath"
	"testing"
	"time"
)

// A long range collapses into <= maxStatPoints buckets without losing any count.
func TestGetStatsSinceBucketsLongRanges(t *testing.T) {
	d, err := NewDatabase(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	const days = 7
	now := time.Now()
	tx, _ := d.db.Begin()
	wantMsgs, n := 0, days*24*60 // one snapshot per minute
	for i := 0; i < n; i++ {
		ts := now.Add(-time.Duration(n-i) * time.Minute)
		if _, err := tx.Exec(`INSERT INTO bot_stats (timestamp, network, messages, user_count) VALUES (?, 'net', 2, ?)`, ts, i%7); err != nil {
			t.Fatal(err)
		}
		wantMsgs += 2
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := d.GetStatsSince(now.AddDate(0, 0, -days), "net")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxStatPoints+2 || len(got) < maxStatPoints/2 {
		t.Fatalf("got %d points for %d rows, want ~%d", len(got), n, maxStatPoints)
	}
	sum, peak := 0, 0
	for i, e := range got {
		sum += e.Messages
		peak = max(peak, e.UserCount)
		if i > 0 && !e.Timestamp.After(got[i-1].Timestamp) {
			t.Fatalf("points not chronological at %d", i)
		}
	}
	if sum != wantMsgs {
		t.Fatalf("messages summed to %d, want %d (bucketing must not lose counts)", sum, wantMsgs)
	}
	if peak != 6 {
		t.Fatalf("user_count peak %d, want 6 (gauges keep their max)", peak)
	}
}

// Short ranges (the live dashboard window) are returned row for row.
func TestGetStatsSinceShortRangeUnchanged(t *testing.T) {
	d, err := NewDatabase(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now()
	for i := 0; i < 60; i++ {
		_ = d.SaveEntry(StatEntry{Timestamp: now.Add(-time.Duration(60-i) * time.Minute), Network: "net", Messages: 1})
	}
	got, _ := d.GetStatsSince(now.Add(-2*time.Hour), "net")
	if len(got) != 60 {
		t.Fatalf("got %d rows, want 60", len(got))
	}
}
