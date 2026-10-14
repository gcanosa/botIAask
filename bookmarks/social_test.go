package bookmarks

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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

// Reminder lookups go through owner_fold: case/bracket variants of a nick are one owner, and
// another nick's reminders never leak in.
func TestReminderOwnerFold(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.AddReminder(testNet, "Al[ice]", "mine", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddReminder(testNet, "bob", "his", nil); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListReminders(testNet, "al{ice}")
	if err != nil || len(got) != 1 || got[0].Note != "mine" {
		t.Fatalf("ListReminders = %+v, %v", got, err)
	}
	join, err := d.ListJoinReminders(testNet, "AL[ICE]")
	if err != nil || len(join) != 1 {
		t.Fatalf("ListJoinReminders = %+v, %v", join, err)
	}
	if n, _ := d.CountRemindersFor(testNet, "alice"); n != 0 {
		t.Fatalf("alice != Al[ice] folded; CountRemindersFor = %d", n)
	}
	if n, _ := d.CountRemindersFor(testNet, "al{ice}"); n != 1 {
		t.Fatalf("CountRemindersFor = %d, want 1", n)
	}
}

// A DB created before owner_fold existed is backfilled on open and re-opening is a no-op.
func TestReminderOwnerFoldMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	d, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddReminder(testNet, "Carol", "old", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE reminders SET owner_fold = ''`); err != nil { // simulate a pre-migration row
		t.Fatal(err)
	}
	d.Close()
	for i := 0; i < 2; i++ { // second open proves idempotence
		d, err = NewDatabase(path)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := d.ListReminders(testNet, "carol")
		if len(got) != 1 {
			t.Fatalf("open %d: backfill missing, got %+v", i, got)
		}
		d.Close()
	}
}

// Text of private messages stored by older versions is wiped on open.
func TestSeenScrubsPrivateText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	d, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.RecordSeen(testNet, "dave", "", "message", "secret pm")
	_ = d.RecordSeen(testNet, "erin", "#chan", "message", "public line")
	d.Close()
	d, err = NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if s, _, _ := d.GetSeen(testNet, "dave"); s.Message != "" {
		t.Fatalf("private text survived: %q", s.Message)
	}
	if s, _, _ := d.GetSeen(testNet, "erin"); s.Message != "public line" {
		t.Fatalf("channel text lost: %q", s.Message)
	}
}

// A production database created before owner_fold existed (reminders without the column) upgrades
// in place on open: column added, rows backfilled, lookups work.
func TestOpenUpgradesPreOwnerFoldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE reminders (
			id INTEGER PRIMARY KEY AUTOINCREMENT, public_id TEXT NOT NULL UNIQUE, owner_nick TEXT NOT NULL,
			note TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, due_at DATETIME,
			network TEXT NOT NULL DEFAULT '');
		INSERT INTO reminders (public_id, owner_nick, note, network) VALUES ('aaaa1111', 'Old[Nick]', 'from the old schema', 'libera');`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	d, err := NewDatabase(path)
	if err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	defer d.Close()
	got, err := d.ListReminders("libera", "old{nick}")
	if err != nil || len(got) != 1 || got[0].Note != "from the old schema" {
		t.Fatalf("old reminder not found after upgrade: %+v %v", got, err)
	}
}
