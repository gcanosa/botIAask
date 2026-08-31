package bookmarks

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *Database {
	t.Helper()
	d, err := NewDatabase(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

const testNet = "testnet"

func TestTimedReminderDue(t *testing.T) {
	d := newTestDB(t)

	// On-join reminder (nil due) must not appear in DueReminders.
	if _, err := d.AddReminder(testNet, "alice", "join note", nil); err != nil {
		t.Fatal(err)
	}
	// Past-due timed reminder must appear.
	past := time.Now().Add(-time.Minute)
	if _, err := d.AddReminder(testNet, "alice", "ping me", &past); err != nil {
		t.Fatal(err)
	}
	// Future timed reminder must not appear yet.
	future := time.Now().Add(time.Hour)
	if _, err := d.AddReminder(testNet, "alice", "later", &future); err != nil {
		t.Fatal(err)
	}

	due, err := d.DueReminders(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Note != "ping me" {
		t.Fatalf("expected 1 due reminder 'ping me', got %+v", due)
	}

	join, err := d.ListJoinReminders(testNet, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(join) != 1 || join[0].Note != "join note" {
		t.Fatalf("expected 1 on-join reminder, got %+v", join)
	}
}

func TestTellRoundTrip(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.AddTell(testNet, "bob", "Alice", "hi there"); err != nil {
		t.Fatal(err)
	}
	// Case-insensitive recipient match.
	n, err := d.CountTellsFor(testNet, "alice")
	if err != nil || n != 1 {
		t.Fatalf("CountTellsFor=%d err=%v, want 1", n, err)
	}
	got, err := d.TakeTells(testNet, "ALICE")
	if err != nil || len(got) != 1 || got[0].Message != "hi there" {
		t.Fatalf("TakeTells=%+v err=%v", got, err)
	}
	// One-shot: second take is empty.
	again, _ := d.TakeTells(testNet, "alice")
	if len(again) != 0 {
		t.Fatalf("expected no tells after delivery, got %+v", again)
	}
}

func TestSeenUpsert(t *testing.T) {
	d := newTestDB(t)
	if err := d.RecordSeen(testNet, "Carol", "#chan", "message", "first"); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordSeen(testNet, "carol", "#chan", "message", "second"); err != nil {
		t.Fatal(err)
	}
	s, ok, err := d.GetSeen(testNet, "CAROL")
	if err != nil || !ok {
		t.Fatalf("GetSeen ok=%v err=%v", ok, err)
	}
	if s.Message != "second" {
		t.Fatalf("expected latest message 'second', got %q", s.Message)
	}
}

func TestBookmarkScopedByNetwork(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.AddBookmark("neta", "https://example.com/x", "alice", "host"); err != nil {
		t.Fatalf("AddBookmark on neta: %v", err)
	}
	if _, err := d.AddBookmark("netb", "https://example.com/x", "bob", "host"); err != nil {
		t.Fatalf("expected same URL bookmarkable on a different network, got: %v", err)
	}
	if _, err := d.AddBookmark("neta", "https://example.com/x", "alice", "host"); err == nil {
		t.Fatal("expected UNIQUE constraint error re-bookmarking the same URL on the same network")
	}
}

func TestSeenScopedByNetwork(t *testing.T) {
	d := newTestDB(t)
	if err := d.RecordSeen("neta", "Dave", "#chan", "message", "on neta"); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordSeen("netb", "Dave", "#chan", "message", "on netb"); err != nil {
		t.Fatal(err)
	}
	sa, ok, err := d.GetSeen("neta", "dave")
	if err != nil || !ok || sa.Message != "on neta" {
		t.Fatalf("neta GetSeen = %+v ok=%v err=%v", sa, ok, err)
	}
	sb, ok, err := d.GetSeen("netb", "dave")
	if err != nil || !ok || sb.Message != "on netb" {
		t.Fatalf("netb GetSeen = %+v ok=%v err=%v", sb, ok, err)
	}
}
