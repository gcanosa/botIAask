package rss

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Database struct {
	db *sql.DB
}

type NewsEntry struct {
	GUID      string
	Title     string
	PubDate   time.Time
	Link      string
	ShortLink string
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS seen_news (
			guid TEXT PRIMARY KEY,
			title TEXT,
			link TEXT,
			short_link TEXT,
			pub_date DATETIME,
			added_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	// Migration: Add columns if they don't exist
	_, _ = db.Exec("ALTER TABLE seen_news ADD COLUMN link TEXT")
	_, _ = db.Exec("ALTER TABLE seen_news ADD COLUMN short_link TEXT")
	// Fix NULL links from migration
	_, _ = db.Exec("UPDATE seen_news SET link = '' WHERE link IS NULL")

	return &Database{db: db}, nil
}

func (d *Database) GetEntryStatus(guid string) (exists bool, hasLink bool, err error) {
	var link sql.NullString
	err = d.db.QueryRow("SELECT link FROM seen_news WHERE guid = ?", guid).Scan(&link)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, link.Valid && link.String != "", nil
}

func (d *Database) UpdateLinks(guid, link, shortLink string) error {
	_, err := d.db.Exec("UPDATE seen_news SET link = ?, short_link = ? WHERE guid = ?",
		link, shortLink, guid)
	return err
}

func (d *Database) IsSeen(guid string) (bool, error) {
	var exists bool
	err := d.db.QueryRow("SELECT EXISTS(SELECT 1 FROM seen_news WHERE guid = ?)", guid).Scan(&exists)
	return exists, err
}

func (d *Database) MarkSeen(entry NewsEntry) error {
	if entry.GUID == "" {
		return fmt.Errorf("cannot mark news as seen with empty GUID")
	}
	_, err := d.db.Exec("INSERT INTO seen_news (guid, title, link, short_link, pub_date) VALUES (?, ?, ?, ?, ?)",
		entry.GUID, entry.Title, entry.Link, entry.ShortLink, entry.PubDate)
	return err
}

func (d *Database) GetLastNews(limit int) ([]NewsEntry, error) {
	rows, err := d.db.Query("SELECT guid, title, COALESCE(link, ''), COALESCE(short_link, ''), pub_date FROM seen_news ORDER BY added_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []NewsEntry
	for rows.Next() {
		var e NewsEntry
		if err := rows.Scan(&e.GUID, &e.Title, &e.Link, &e.ShortLink, &e.PubDate); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (d *Database) Cleanup(keep int) error {
	_, err := d.db.Exec(`
		DELETE FROM seen_news 
		WHERE guid NOT IN (
			SELECT guid FROM seen_news 
			ORDER BY added_at DESC 
			LIMIT ?
		)
	`, keep)
	return err
}

func (d *Database) DropAll() error {
	_, err := d.db.Exec("DELETE FROM seen_news")
	return err
}

func (d *Database) Close() error {
	return d.db.Close()
}
