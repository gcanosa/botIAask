package rss

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

func TestNormalizeRSSLink_TrackingAndHTTPS(t *testing.T) {
	base := "https://example.com/article?utm_source=twitter&foo=1&utm_medium=social&foo=2#frag"
	got := NormalizeRSSLink(base)
	want := "https://example.com/article?foo=1&foo=2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDedupKeyStableUnderGUIDChange(t *testing.T) {
	src := "arstechnica"
	link := "https://example.com/story?utm_campaign=x"
	norm := NormalizeRSSLink(link)
	k1 := DedupKeyFromParts(src, norm, "guid-v1", "Title")
	k2 := DedupKeyFromParts(src, norm, "guid-v2", "Title")
	if k1 != k2 {
		t.Fatalf("dedup should not depend on guid when link is present: %s vs %s", k1, k2)
	}
}

func TestNewsItemDuplicate_DedupKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rss.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	e := NewsEntry{
		GUID:           "volatile-guid-1",
		Title:          "Hello",
		Link:           "https://ex.com/a?utm_source=x",
		ShortLink:      "x",
		PubDate:        time.Now(),
		Source:         "testsrc",
		LinkNormalized: NormalizeRSSLink("https://ex.com/a?utm_source=x"),
		DedupKey:       DedupKeyFromParts("testsrc", NormalizeRSSLink("https://ex.com/a?utm_source=x"), "volatile-guid-1", "Hello"),
	}
	if err := db.MarkSeen(e); err != nil {
		t.Fatal(err)
	}
	dup, err := db.NewsItemDuplicate("different-guid", e.DedupKey, e.LinkNormalized)
	if err != nil {
		t.Fatal(err)
	}
	if !dup {
		t.Fatal("expected duplicate via dedup_key / link_normalized")
	}
}

func TestEntryFromFeedItem(t *testing.T) {
	it := &gofeed.Item{
		GUID:            "abc",
		Link:            "https://site.com/p?utm_medium=email",
		Title:           "T",
		PublishedParsed: nil,
	}
	e, ok := EntryFromFeedItem(it, "mysrc", "")
	if !ok || e.GUID != "abc" || e.DedupKey == "" || e.LinkNormalized == "" {
		t.Fatalf("entry %+v ok=%v", e, ok)
	}
}

func TestCleanupPerSource(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rss2.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i, src := range []string{"a", "a", "a", "b", "b"} {
		link := fmt.Sprintf("https://x.com/%d", i)
		e := NewsEntry{
			GUID:           fmt.Sprintf("g%d", i),
			Title:          "t",
			Link:           link,
			PubDate:        time.Now().Add(time.Duration(-i) * time.Minute),
			Source:         src,
			LinkNormalized: NormalizeRSSLink(link),
			DedupKey:       DedupKeyFromParts(src, NormalizeRSSLink(link), "", "t"),
		}
		if err := db.MarkSeen(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CleanupPerSource(2); err != nil {
		t.Fatal(err)
	}
	n, err := db.CountSeenNews()
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("want 4 rows after per-source keep=2 (3+2), got %d", n)
	}
}
