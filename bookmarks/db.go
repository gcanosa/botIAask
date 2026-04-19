package bookmarks

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Bookmark struct {
	ID        int       `json:"id"`
	URL       string    `json:"url"`
	Nickname  string    `json:"nickname"`
	Hostname  string    `json:"hostname"`
	Timestamp time.Time `json:"timestamp"`
}

type Database struct {
	db *sql.DB
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open bookmarks database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS bookmarks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT UNIQUE,
			nickname TEXT,
			hostname TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create bookmarks table: %w", err)
	}

	return &Database{db: db}, nil
}

func (d *Database) AddBookmark(url, nickname, hostname string) error {
	_, err := d.db.Exec("INSERT INTO bookmarks (url, nickname, hostname) VALUES (?, ?, ?)",
		url, nickname, hostname)
	return err
}

func (d *Database) GetBookmarksCount() (int, error) {
	var count int
	err := d.db.QueryRow("SELECT COUNT(*) FROM bookmarks").Scan(&count)
	return count, err
}

func (d *Database) GetBookmarks(limit, offset int) ([]Bookmark, error) {
	rows, err := d.db.Query("SELECT id, url, nickname, hostname, timestamp FROM bookmarks ORDER BY timestamp DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bookmarks []Bookmark
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.URL, &b.Nickname, &b.Hostname, &b.Timestamp); err != nil {
			return nil, err
		}
		bookmarks = append(bookmarks, b)
	}
	return bookmarks, nil
}

func (d *Database) CountUserBookmarksSince(nickname string, since time.Time) (int, error) {
	var count int
	err := d.db.QueryRow("SELECT COUNT(*) FROM bookmarks WHERE nickname = ? AND timestamp > ?", 
		nickname, since).Scan(&count)
	return count, err
}

func (d *Database) Close() error {
	return d.db.Close()
}
