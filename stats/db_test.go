package stats

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestNewDatabase_migratesOldSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "stats.db")

	// Old schema: missing admin columns (matches pre-migration deploys)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS bot_stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			messages INTEGER DEFAULT 0,
			actions INTEGER DEFAULT 0,
			ai_requests INTEGER DEFAULT 0,
			user_count INTEGER DEFAULT 0,
			joins INTEGER DEFAULT 0,
			parts INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_stats_timestamp ON bot_stats(timestamp);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	sdb, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer sdb.Close()

	rows, err := sdb.db.Query(`PRAGMA table_info(bot_stats)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		names[name] = struct{}{}
	}
	for _, col := range []string{"admin_commands", "logged_in_admins", "failed_auths"} {
		if _, ok := names[col]; !ok {
			t.Errorf("missing column %q after migration", col)
		}
	}
}

func TestGetRecentStats_chronologicalOrder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "stats.db")
	sdb, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer sdb.Close()

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		e := StatEntry{Timestamp: base.Add(time.Duration(i) * time.Minute), Messages: i}
		if err := sdb.SaveEntry(e); err != nil {
			t.Fatalf("SaveEntry %d: %v", i, err)
		}
	}

	entries, err := sdb.GetRecentStats(5)
	if err != nil {
		t.Fatalf("GetRecentStats: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5", len(entries))
	}
	for i := 0; i < len(entries)-1; i++ {
		if entries[i].Timestamp.After(entries[i+1].Timestamp) {
			t.Fatalf("entries not in chronological order at index %d: %v after %v", i, entries[i].Timestamp, entries[i+1].Timestamp)
		}
	}
	if entries[0].Messages != 0 || entries[4].Messages != 4 {
		t.Fatalf("unexpected order: first.Messages=%d last.Messages=%d", entries[0].Messages, entries[4].Messages)
	}
}
