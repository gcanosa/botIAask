package uploads

import (
	"fmt"
	"testing"
	"time"
)

func approvePaste(t *testing.T, d *Database, id string, days int) {
	t.Helper()
	tok := "tok-" + id
	if err := d.CreateUploadSession(tok, "nick", "#c", "net"); err != nil {
		t.Fatal(err)
	}
	if err := d.SubmitUpload(tok, id, "title "+id, "d", "body", days, "host"); err != nil {
		t.Fatal(err)
	}
	if err := d.ApproveTicket(id); err != nil {
		t.Fatal(err)
	}
}

// Paging happens in SQL but must give the same answer the old load-everything filter did:
// non-staff lists skip pastes past their expiry, staff lists keep them, totals match the pages.
func TestGetApprovedPastesPagesInSQL(t *testing.T) {
	path := t.TempDir() + "/u.db"
	d, err := NewDatabase(path, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	for i := 0; i < 25; i++ {
		days := 0 // never expires
		if i%5 == 0 {
			days = 1 // these five will be aged below
		}
		approvePaste(t, d, fmt.Sprintf("p%02d", i), days)
	}
	// age the 1-day pastes to 3 days ago; keep approved_at ordering distinct for the rest
	old := time.Now().Add(-72 * time.Hour)
	for i := 0; i < 25; i += 5 {
		if _, err := d.db.Exec(`UPDATE uploads SET approved_at = ?, expires_at = ? WHERE ticket_id = ?`,
			old, old.Add(24*time.Hour).Unix(), fmt.Sprintf("p%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()

	var seen []string
	for off := 0; ; off += 10 {
		page, total, err := d.GetApprovedPastes(10, off, now, true)
		if err != nil {
			t.Fatal(err)
		}
		if total != 20 {
			t.Fatalf("public total = %d, want 20 (5 expired hidden)", total)
		}
		if len(page) == 0 {
			break
		}
		for _, u := range page {
			if u.IsAccessExpired(now) {
				t.Fatalf("expired paste %s listed publicly", u.TicketID)
			}
			seen = append(seen, u.TicketID)
		}
	}
	if len(seen) != 20 {
		t.Fatalf("paged through %d pastes, want 20", len(seen))
	}
	if page, total, _ := d.GetApprovedPastes(10, 0, now, false); total != 25 || len(page) != 10 {
		t.Fatalf("staff view: total=%d page=%d, want 25/10", total, len(page))
	}
	if page, total, _ := d.GetApprovedPastes(10, 100, now, false); total != 25 || len(page) != 0 {
		t.Fatalf("past the end: total=%d page=%d", total, len(page))
	}
}

// Rows approved before the expires_at column existed are filled in when the database is opened.
func TestExpiresAtBackfill(t *testing.T) {
	path := t.TempDir() + "/u.db"
	pastes, files := t.TempDir(), t.TempDir()
	d, err := NewDatabase(path, pastes, files)
	if err != nil {
		t.Fatal(err)
	}
	approvePaste(t, d, "legacy", 7)
	if _, err := d.db.Exec(`UPDATE uploads SET expires_at = NULL WHERE ticket_id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d, err = NewDatabase(path, pastes, files)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var exp int64
	if err := d.db.QueryRow(`SELECT expires_at FROM uploads WHERE ticket_id = 'legacy'`).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	if want := time.Now().Add(7 * 24 * time.Hour).Unix(); exp < want-60 || exp > want+60 {
		t.Fatalf("backfilled expires_at = %d, want ~%d", exp, want)
	}
}
