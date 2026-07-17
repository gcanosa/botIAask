package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackupAndPrune(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "thing.db")
	conn, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE t(x); INSERT INTO t VALUES (1);`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three snapshots with distinct (lexically ordered) timestamps.
	for _, name := range []string{"thing-20240101-000000.db", "thing-20240102-000000.db", "thing-20240103-000000.db"} {
		if err := backupOne(src, filepath.Join(backupDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	pruneBackups(backupDir, "thing", 2)

	got, _ := filepath.Glob(filepath.Join(backupDir, "thing-*.db"))
	if len(got) != 2 {
		t.Fatalf("expected 2 backups after prune, got %d", len(got))
	}
	for _, g := range got {
		if filepath.Base(g) == "thing-20240101-000000.db" {
			t.Fatal("oldest backup should have been pruned")
		}
	}
}
