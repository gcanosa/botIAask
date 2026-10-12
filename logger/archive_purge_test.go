package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgeArchives(t *testing.T) {
	old := logsDir
	logsDir = t.TempDir()
	defer func() { logsDir = old }()
	dir := filepath.Join(logsDir, "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local)
	files := map[string]bool{ // name -> should survive
		"libera_go_2026-07-01.log.gz":  false, // > 90 days old
		"libera_go_2026-07-13.log.gz":  true,  // exactly at the cutoff
		"libera_go_2026-10-01.log.gz":  true,
		"notes.txt":                    true, // not an archive
		"libera_go_bad-date-xx.log.gz": true,
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	purgeArchives(logsDir, 90, now)
	for name, keep := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		if exists := err == nil; exists != keep {
			t.Errorf("%s: exists=%v, want %v", name, exists, keep)
		}
	}
}
