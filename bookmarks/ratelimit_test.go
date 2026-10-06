package bookmarks

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCountUserBookmarksSince(t *testing.T) {
	d, err := NewDatabase(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddBookmark("n", "http://a.example/1", "nick", "h"); err != nil {
		t.Fatal(err)
	}
	n, err := d.CountUserBookmarksSince("n", "nick", time.Now().Add(-10*time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("recent bookmark not counted: n=%d err=%v", n, err)
	}
	n, _ = d.CountUserBookmarksSince("n", "nick", time.Now().Add(time.Minute))
	if n != 0 {
		t.Fatalf("future cutoff should count 0, got %d", n)
	}
}
