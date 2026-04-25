package stats

import (
	"database/sql"
	"path/filepath"
	"testing"

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
