package db

import (
	"path/filepath"
	"testing"
)

// TestPragmasAppliedPerConnection guards against the regression where pragmas were
// applied via a one-off db.Exec after opening — that only lands on whichever pooled
// connection happens to run it, so most of a busy pool would silently run with
// busy_timeout=0 (immediate SQLITE_BUSY instead of waiting). Pragmas must instead
// travel in the DSN so the driver applies them to every new connection it opens.
func TestPragmasAppliedPerConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pragma_test.db")
	sqldb, err := OpenDatabase(dbPath)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer sqldb.Close()

	// Force a fresh connection on each query so we're not just observing the same conn twice.
	sqldb.SetMaxIdleConns(0)

	for i := 0; i < 2; i++ {
		var busyTimeout int
		if err := sqldb.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("query busy_timeout (iter %d): %v", i, err)
		}
		if busyTimeout != 5000 {
			t.Errorf("iter %d: busy_timeout = %d, want 5000", i, busyTimeout)
		}

		var foreignKeys int
		if err := sqldb.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("query foreign_keys (iter %d): %v", i, err)
		}
		if foreignKeys != 1 {
			t.Errorf("iter %d: foreign_keys = %d, want 1", i, foreignKeys)
		}
	}
}
