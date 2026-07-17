package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// startBackupScheduler periodically snapshots every SQLite DB in dataDir into
// dataDir/backups using VACUUM INTO (WAL-safe, consistent), keeping the newest
// `keep` snapshots per DB. Blocks; run it in a goroutine.
func startBackupScheduler(dataDir string, intervalHrs, keep int) {
	if intervalHrs <= 0 {
		intervalHrs = 24
	}
	if keep <= 0 {
		keep = 7
	}
	runBackup(dataDir, keep) // one snapshot shortly after start
	ticker := time.NewTicker(time.Duration(intervalHrs) * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		runBackup(dataDir, keep)
	}
}

func runBackup(dataDir string, keep int) {
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		log.Printf("backup: mkdir %s: %v", backupDir, err)
		return
	}
	dbFiles, err := filepath.Glob(filepath.Join(dataDir, "*.db"))
	if err != nil {
		log.Printf("backup: glob: %v", err)
		return
	}
	stamp := time.Now().Format("20060102-150405")
	for _, src := range dbFiles {
		name := strings.TrimSuffix(filepath.Base(src), ".db")
		dest := filepath.Join(backupDir, fmt.Sprintf("%s-%s.db", name, stamp))
		if err := backupOne(src, dest); err != nil {
			log.Printf("backup: %s: %v", name, err)
			continue
		}
		pruneBackups(backupDir, name, keep)
	}
}

// backupOne writes a consistent snapshot of src to dest via VACUUM INTO.
func backupOne(src, dest string) error {
	// busy_timeout so VACUUM INTO waits out a concurrent writer instead of failing SQLITE_BUSY.
	conn, err := sql.Open("sqlite", src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer conn.Close()
	// VACUUM INTO fails if dest already exists; the timestamped name avoids that.
	_, err = conn.Exec("VACUUM INTO ?", dest)
	return err
}

// pruneBackups deletes oldest snapshots for one DB beyond `keep`. The timestamp
// suffix is fixed-width, so lexical order == chronological order.
func pruneBackups(backupDir, name string, keep int) {
	matches, err := filepath.Glob(filepath.Join(backupDir, name+"-*.db"))
	if err != nil || len(matches) <= keep {
		return
	}
	sort.Strings(matches)
	for _, old := range matches[:len(matches)-keep] {
		if err := os.Remove(old); err != nil {
			log.Printf("backup: prune %s: %v", old, err)
		}
	}
}
