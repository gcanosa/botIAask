package uploads

import (
	"database/sql"
	"errors"
	"testing"
)

// A token may cancel only a not-yet-submitted upload; once submitted it must go through moderation.
func TestCancelUploadByTokenOnlyWhilePendingForm(t *testing.T) {
	d, err := NewDatabase(t.TempDir()+"/u.db", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.CreateUploadSession("tok1", "nick", "#c", "net"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.CancelUploadByToken("tok1"); err != nil {
		t.Fatalf("pending_form cancel: %v", err)
	}
	if _, _, _, err := d.CancelUploadByToken("tok1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second cancel: want ErrNoRows, got %v", err)
	}

	if err := d.CreateUploadSession("tok2", "nick", "#c", "net"); err != nil {
		t.Fatal(err)
	}
	if err := d.SubmitUpload("tok2", "tick2", "t", "d", "body", 7, "host"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.CancelUploadByToken("tok2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("submitted upload must not be cancellable by token, got %v", err)
	}
}
