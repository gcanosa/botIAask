package shortlinks

import (
	"path/filepath"
	"testing"
	"time"
)

func TestShortenLookupDedupeAndValidation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	code, err := s.Shorten("https://example.com/a?b=c")
	if err != nil || !ValidCode(code) || len(code) != codeLen {
		t.Fatalf("Shorten = %q, %v", code, err)
	}
	if again, _ := s.Shorten("https://example.com/a?b=c"); again != code {
		t.Errorf("same URL got a new code: %q vs %q", again, code)
	}
	if got, ok := s.Lookup(code); !ok || got != "https://example.com/a?b=c" {
		t.Errorf("Lookup = %q, %v", got, ok)
	}
	if _, ok := s.Lookup("nope123"); ok {
		t.Error("unknown code resolved")
	}
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "example.com", "", "http://"} {
		if _, err := s.Shorten(bad); err != ErrBadURL {
			t.Errorf("Shorten(%q) err = %v, want ErrBadURL", bad, err)
		}
	}
}

func TestPurgeKeepsUsedLinks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, _ := s.Shorten("https://example.com/old")
	used, _ := s.Shorten("https://example.com/used")
	fresh, _ := s.Shorten("https://example.com/fresh")
	ancient := time.Now().Add(-100 * 24 * time.Hour).Unix()
	recent := time.Now().Add(-10 * 24 * time.Hour).Unix()
	s.db.Exec(`UPDATE links SET created_at = ? WHERE code IN (?, ?)`, ancient, old, used)
	s.db.Exec(`UPDATE links SET last_used = ? WHERE code = ?`, recent, used)

	n, err := s.Purge(90 * 24 * time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("Purge = %d, %v; want 1", n, err)
	}
	if _, ok := s.Lookup(old); ok {
		t.Error("expired link survived")
	}
	for _, c := range []string{used, fresh} {
		if _, ok := s.Lookup(c); !ok {
			t.Errorf("link %s was purged", c)
		}
	}
}
