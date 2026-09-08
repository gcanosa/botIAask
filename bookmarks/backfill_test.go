package bookmarks

import "testing"

// TestBackfillLegacyNetwork exercises the upgrade path: rows left with network = ” by the
// schema-rebuild migrations (or genuinely predating multi-network support) must become
// visible under the resolved single-network name, and a legacy row that collides with one
// already recorded under that name must lose to it rather than error out or clobber it.
func TestBackfillLegacyNetwork(t *testing.T) {
	d := newTestDB(t)

	// Simulate legacy (pre-migration) rows by inserting directly with network = ''.
	if _, err := d.db.Exec(`INSERT INTO bookmarks (network, url, nickname, hostname) VALUES ('', 'https://example.com/a', 'alice', 'host')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO seen (network, nick_fold, nick, channel, action, message, last_seen) VALUES ('', 'alice', 'alice', '#chan', 'message', 'hi', datetime('now', '-1 hour'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO tells (public_id, from_nick, to_fold, to_nick, message, network) VALUES ('t1', 'bob', 'alice', 'alice', 'hey', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddReminder(testNet, "alice", "will be re-tagged below", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE reminders SET network = ''`); err != nil {
		t.Fatal(err)
	}

	// A genuine conflict: activity already recorded under the real network name for the
	// same key as a legacy row (bookmarks: same URL; seen: same nick_fold), simulating a
	// deployment that ran briefly on a pre-backfill multi-network build.
	if _, err := d.AddBookmark(testNet, "https://example.com/a", "alice", "host"); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordSeen(testNet, "alice", "#chan", "message", "more recent"); err != nil {
		t.Fatal(err)
	}

	if err := d.BackfillLegacyNetwork(testNet); err != nil {
		t.Fatalf("BackfillLegacyNetwork: %v", err)
	}

	// No orphaned '' rows should remain in any of the four tables.
	for _, table := range []string{"bookmarks", "seen", "tells", "reminders"} {
		var n int
		if err := d.db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE network = ''").Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s: %d rows still have network = '' after backfill", table, n)
		}
	}

	// The conflict winner must be the already-live row, not the legacy duplicate.
	seen, ok, err := d.GetSeen(testNet, "alice")
	if err != nil || !ok {
		t.Fatalf("GetSeen after backfill: ok=%v err=%v", ok, err)
	}
	if seen.Message != "more recent" {
		t.Errorf("seen conflict: want live row's message %q, got %q", "more recent", seen.Message)
	}

	// The non-conflicting tell/reminder must have been re-tagged and be visible under testNet.
	tells, err := d.TakeTells(testNet, "alice")
	if err != nil {
		t.Fatalf("TakeTells: %v", err)
	}
	if len(tells) != 1 {
		t.Fatalf("expected 1 backfilled tell, got %d", len(tells))
	}
}
