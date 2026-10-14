package uploads

import (
	"errors"
	"os"
	"testing"
	"time"
)

// A ticket id that collides with an existing paste must neither overwrite nor delete that paste.
func TestSubmitUploadNeverClobbersExistingPaste(t *testing.T) {
	pastes := t.TempDir()
	d, err := NewDatabase(t.TempDir()+"/u.db", pastes, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	for _, tok := range []string{"tok1", "tok2"} {
		if err := d.CreateUploadSession(tok, "nick", "#c", "net"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SubmitUpload("tok1", "dup", "t", "d", "first paste", 7, "h"); err != nil {
		t.Fatal(err)
	}
	if err := d.SubmitUpload("tok2", "dup", "t", "d", "second paste", 7, "h"); !errors.Is(err, ErrTicketIDTaken) {
		t.Fatalf("colliding id: want ErrTicketIDTaken, got %v", err)
	}
	got, err := os.ReadFile(pastes + "/dup.txt")
	if err != nil || string(got) != "first paste" {
		t.Fatalf("first paste damaged: %q, %v", got, err)
	}
	// the refused session is still usable with another id
	if err := d.SubmitUpload("tok2", "other", "t", "d", "second paste", 7, "h"); err != nil {
		t.Fatalf("retry with a new id: %v", err)
	}
}

// Absurd expiry values stored by older versions must not overflow time.Duration into "already expired".
func TestAccessExpiresAtSurvivesHugeValues(t *testing.T) {
	u := &Upload{ExpiresInDays: 1 << 40}
	u.ApprovedAt.Valid, u.ApprovedAt.Time = true, time.Now()
	if u.IsAccessExpired(time.Now()) {
		t.Fatal("huge expiry window reads as already expired")
	}
}

func TestOpenSessionCountAndStalePurge(t *testing.T) {
	d, err := NewDatabase(t.TempDir()+"/u.db", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, tok := range []string{"a", "b", "c"} {
		if err := d.CreateUploadSession(tok, "nick", "#c", "net"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.CreateUploadSession("other", "someone", "#c", "net"); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountOpenSessions("nick", "net"); n != 3 {
		t.Fatalf("open sessions = %d, want 3", n)
	}
	if n, _ := d.CountOpenSessions("nick", "othernet"); n != 0 {
		t.Fatalf("other network counted: %d", n)
	}
	// age two sessions past SessionTTL: they no longer count as open
	if _, err := d.db.Exec(`UPDATE uploads SET created_at = ? WHERE token IN ('a','b')`, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountOpenSessions("nick", "net"); n != 1 {
		t.Fatalf("after expiry open sessions = %d, want 1", n)
	}
	// a submitted (pending approval) ticket must survive the purge even when old
	if err := d.SubmitUpload("c", "tkt", "t", "d", "body", 7, "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE uploads SET created_at = ? WHERE token = 'c'`, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := d.PurgeStaleSessions(24 * time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("purged %d (%v), want the 2 stale unused links", n, err)
	}
	if _, err := d.GetUploadByTicketID("tkt"); err != nil {
		t.Fatalf("pending-approval ticket was purged: %v", err)
	}
	if _, err := d.GetUploadByToken("other"); err != nil {
		t.Fatalf("fresh session was purged: %v", err)
	}
}
