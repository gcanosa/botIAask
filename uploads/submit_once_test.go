package uploads

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSubmitUploadOnlyOncePerToken(t *testing.T) {
	pastes := t.TempDir()
	d, err := NewDatabase(t.TempDir()+"/u.db", pastes, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateUploadSession("tok", "u", "#c", "n"); err != nil {
		t.Fatal(err)
	}
	if err := d.SubmitUpload("tok", "aaaa", "t", "d", "body", 7, "h"); err != nil {
		t.Fatal(err)
	}
	err = d.SubmitUpload("tok", "bbbb", "t", "d", "body", 7, "h")
	if !errors.Is(err, ErrTokenUsed) {
		t.Fatalf("second submit: want ErrTokenUsed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(pastes, "bbbb.txt")); !os.IsNotExist(err) {
		t.Fatal("orphan file left behind by rejected submit")
	}
}

func TestApproveOnlyPending(t *testing.T) {
	d, err := NewDatabase(t.TempDir()+"/u.db", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.CreateUploadSession("tok", "u", "#c", "n")
	d.SubmitUpload("tok", "aaaa", "t", "d", "body", 7, "h")
	if err := d.ApproveTicket("aaaa"); err != nil {
		t.Fatal(err)
	}
	if err := d.ApproveTicket("aaaa"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("re-approve: want ErrNotPending, got %v", err)
	}
	if err := d.ApproveTicket("nope"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("unknown ticket: want ErrNotPending, got %v", err)
	}
}
