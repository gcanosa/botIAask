// Package shortlinks is the private URL shortener: long URL <-> random code, SQLite-backed.
package shortlinks

import (
	"botIAask/db"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"
)

const (
	codeLen   = 7
	maxURLLen = 2048
	alphabet  = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

// ErrBadURL is returned for anything that is not a plain http(s) URL (blocks javascript:, file:, ...).
var ErrBadURL = errors.New("not an http(s) url")

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	d, err := db.OpenDatabase(path)
	if err != nil {
		return nil, err
	}
	// UNIQUE(url) is the dedupe: RSS/GitHub announce the same URL repeatedly.
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS links (
		code TEXT PRIMARY KEY,
		url TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		last_used INTEGER NOT NULL DEFAULT 0)`); err != nil {
		d.Close()
		return nil, err
	}
	// Migration for DBs created before last_used existed; "duplicate column" is the expected no-op.
	if _, err := d.Exec(`ALTER TABLE links ADD COLUMN last_used INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		d.Close()
		return nil, err
	}
	return &Store{db: d}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ValidCode reports whether code has the shape Shorten produces (cheap pre-check for the redirect route).
func ValidCode(code string) bool {
	if len(code) == 0 || len(code) > 32 {
		return false
	}
	for _, c := range code {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

func newCode() (string, error) {
	b := make([]byte, codeLen)
	n := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b), nil
}

// Shorten returns the code for rawURL, creating it if new.
func (s *Store) Shorten(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(rawURL) > maxURLLen {
		return "", ErrBadURL
	}
	existing := func() (string, bool) {
		var code string
		err := s.db.QueryRow(`SELECT code FROM links WHERE url = ?`, rawURL).Scan(&code)
		return code, err == nil
	}
	if code, ok := existing(); ok {
		s.touch(code) // re-announcing a URL keeps its link alive
		return code, nil
	}
	for i := 0; i < 5; i++ {
		code, err := newCode()
		if err != nil {
			return "", err
		}
		if _, err := s.db.Exec(`INSERT INTO links(code, url, created_at) VALUES(?, ?, ?)`, code, rawURL, time.Now().Unix()); err == nil {
			return code, nil
		}
		// Failed insert = code collision or a concurrent insert of the same url; the latter is a hit.
		if code, ok := existing(); ok {
			return code, nil
		}
	}
	return "", errors.New("could not allocate a short code")
}

func (s *Store) Lookup(code string) (string, bool) {
	var u string
	if err := s.db.QueryRow(`SELECT url FROM links WHERE code = ?`, code).Scan(&u); err != nil {
		return "", false
	}
	s.touch(code)
	return u, true
}

// touch records a use, at most once a day per link so a popular link is not a write per click.
func (s *Store) touch(code string) {
	now := time.Now().Unix()
	s.db.Exec(`UPDATE links SET last_used = ? WHERE code = ? AND last_used < ?`, now, code, now-86400)
}

// Purge deletes links neither created nor used within maxAge and returns how many were removed.
func (s *Store) Purge(maxAge time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM links WHERE MAX(created_at, last_used) < ?`, time.Now().Add(-maxAge).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
