package github

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"botIAask/db"
)

func newTestDB(t *testing.T) *Database {
	t.Helper()
	d, err := NewDatabase(filepath.Join(t.TempDir(), "github_seen.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestEventSeen_DedupRoundTrip(t *testing.T) {
	d := newTestDB(t)
	key := EventKey("owner", "repo", "12345")

	seen, err := d.EventSeen(key)
	if err != nil || seen {
		t.Fatalf("expected not seen before marking, got seen=%v err=%v", seen, err)
	}

	if err := d.MarkEventSeen(key, "owner/repo", "push", "abc1234", "pushed to main", time.Now()); err != nil {
		t.Fatalf("MarkEventSeen: %v", err)
	}
	// Double-mark must not error (INSERT OR IGNORE).
	if err := d.MarkEventSeen(key, "owner/repo", "push", "abc1234", "pushed to main", time.Now()); err != nil {
		t.Fatalf("MarkEventSeen (dup): %v", err)
	}

	seen, err = d.EventSeen(key)
	if err != nil || !seen {
		t.Fatalf("expected seen after marking, got seen=%v err=%v", seen, err)
	}
}

func TestETagRoundTrip(t *testing.T) {
	d := newTestDB(t)

	etag, err := d.GetETag("owner/repo")
	if err != nil || etag != "" {
		t.Fatalf("expected empty etag before Set, got %q err=%v", etag, err)
	}

	if err := d.SetETag("owner/repo", "\"abc123\""); err != nil {
		t.Fatalf("SetETag: %v", err)
	}
	etag, err = d.GetETag("owner/repo")
	if err != nil || etag != "\"abc123\"" {
		t.Fatalf("expected stored etag, got %q err=%v", etag, err)
	}

	if err := d.SetETag("owner/repo", "\"xyz789\""); err != nil {
		t.Fatalf("SetETag (overwrite): %v", err)
	}
	etag, err = d.GetETag("owner/repo")
	if err != nil || etag != "\"xyz789\"" {
		t.Fatalf("expected overwritten etag, got %q err=%v", etag, err)
	}
}

func TestSearchEvents_MatchesMessageOrRefID(t *testing.T) {
	d := newTestDB(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("MarkEventSeen: %v", err)
		}
	}
	must(d.MarkEventSeen("owner/repo#1", "owner/repo", "push", "abc1234", "alice pushed to main", time.Now()))
	must(d.MarkEventSeen("owner/repo#2", "owner/repo", "pull_request", "#42", "bob opened PR: fix bug in parser", time.Now()))
	must(d.MarkEventSeen("owner/repo#3", "other/repo", "push", "def5678", "carol pushed to main", time.Now()))

	byMessage, err := d.SearchEvents("owner/repo", regexp.MustCompile("(?i)parser"), 5)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(byMessage) != 1 || byMessage[0].RefID != "#42" {
		t.Fatalf("expected one match on message text, got %+v", byMessage)
	}

	byRefID, err := d.SearchEvents("owner/repo", regexp.MustCompile("^abc1234$"), 5)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(byRefID) != 1 || byRefID[0].Kind != "push" {
		t.Fatalf("expected one match on ref_id, got %+v", byRefID)
	}

	otherRepo, err := d.SearchEvents("owner/repo", regexp.MustCompile("carol"), 5)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(otherRepo) != 0 {
		t.Fatalf("expected repo filter to exclude other/repo's row, got %+v", otherRepo)
	}
}

func TestSearchEvents_RespectsLimit(t *testing.T) {
	d := newTestDB(t)
	for i := 0; i < 5; i++ {
		key := EventKey("owner", "repo", strconv.Itoa(i))
		if err := d.MarkEventSeen(key, "owner/repo", "push", "sha", "pushed to main", time.Now()); err != nil {
			t.Fatalf("MarkEventSeen: %v", err)
		}
	}
	out, err := d.SearchEvents("owner/repo", regexp.MustCompile("pushed"), 2)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected limit=2 respected, got %d rows", len(out))
	}
}

// TestNewDatabase_MigratesOldSchemaWithoutRefIDOrMessage confirms opening a DB seeded with
// the pre-migration schema (no ref_id/message columns) doesn't error and old rows read
// back with empty strings for the new columns, instead of failing to load entirely.
func TestNewDatabase_MigratesOldSchemaWithoutRefIDOrMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old_schema.db")
	seed, err := db.OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE seen_events (
		event_key TEXT PRIMARY KEY,
		repo TEXT NOT NULL,
		kind TEXT NOT NULL,
		occurred_at DATETIME,
		added_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("seed create table: %v", err)
	}
	if _, err := seed.Exec(`INSERT INTO seen_events (event_key, repo, kind, occurred_at) VALUES (?, ?, ?, ?)`,
		"owner/repo#1", "owner/repo", "push", time.Now()); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	d, err := NewDatabase(path)
	if err != nil {
		t.Fatalf("NewDatabase on old schema: %v", err)
	}
	defer d.Close()

	var refID, message string
	err = d.db.QueryRow(`SELECT ref_id, message FROM seen_events WHERE event_key = ?`, "owner/repo#1").Scan(&refID, &message)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("query migrated row: %v", err)
	}
	if refID != "" || message != "" {
		t.Fatalf("expected empty ref_id/message for pre-migration row, got %q/%q", refID, message)
	}
}
