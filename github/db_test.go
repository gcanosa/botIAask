package github

import (
	"path/filepath"
	"testing"
	"time"
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

	if err := d.MarkEventSeen(key, "owner/repo", "push", time.Now()); err != nil {
		t.Fatalf("MarkEventSeen: %v", err)
	}
	// Double-mark must not error (INSERT OR IGNORE).
	if err := d.MarkEventSeen(key, "owner/repo", "push", time.Now()); err != nil {
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
