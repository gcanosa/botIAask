package reddit

import (
	"database/sql"
	"fmt"
	"strings"

	"botIAask/db"
)

// Database is data/reddit_seen.db: one row per announced (or skipped) post.
type Database struct {
	db *sql.DB
}

func NewDatabase(path string) (*Database, error) {
	sqldb, err := db.OpenDatabase(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if _, err := sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS seen_posts (
			post_id   TEXT PRIMARY KEY,
			sub       TEXT NOT NULL,
			title     TEXT,
			link      TEXT,
			author    TEXT,
			published DATETIME,
			added_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_seen DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_seen_posts_sub ON seen_posts(sub);`); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("failed to create table: %w", err)
	}
	return &Database{db: sqldb}, nil
}

func (d *Database) Close() error { return d.db.Close() }

// Seen reports whether the post is known and, if so, stamps last_seen so Prune keeps rows
// that are still in a live feed (a quiet sub's /new can span months).
func (d *Database) Seen(postID string) (bool, error) {
	res, err := d.db.Exec(`UPDATE seen_posts SET last_seen = CURRENT_TIMESTAMP WHERE post_id = ?`, postID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// HasAny reports whether any post of sub was ever stored (false = first poll of that sub).
func (d *Database) HasAny(sub string) (bool, error) {
	var ok bool
	err := d.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM seen_posts WHERE sub = ?)`, strings.ToLower(sub)).Scan(&ok)
	return ok, err
}

func (d *Database) MarkSeen(p Post) error {
	_, err := d.db.Exec(`INSERT OR IGNORE INTO seen_posts (post_id, sub, title, link, author, published) VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, strings.ToLower(p.Sub), p.Title, p.Link, p.Author, p.Published)
	return err
}

// Prune drops posts not seen in any feed for 14 days: they have left /new for good.
func (d *Database) Prune() error {
	_, err := d.db.Exec(`DELETE FROM seen_posts WHERE last_seen < datetime('now', '-14 days')`)
	return err
}
