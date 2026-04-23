package rss

import (
	"database/sql"
	"fmt"
	"strings"
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
	// Source is a stable key for UI (e.g. "hacker-news"); set from the RSS feed at insert, not from item links.
	Source     string
	SourceIcon string // image URL for web badge; empty for legacy rows
	// LinkNormalized and DedupKey stabilize identity when GUID or link spelling changes (see rss/dedup.go).
	LinkNormalized string
	DedupKey       string
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
	_, _ = db.Exec("ALTER TABLE seen_news ADD COLUMN source TEXT")
	// Fix NULL links from migration
	_, _ = db.Exec("UPDATE seen_news SET link = '' WHERE link IS NULL")
	_, _ = db.Exec("UPDATE seen_news SET source = '' WHERE source IS NULL")
	_, _ = db.Exec("ALTER TABLE seen_news ADD COLUMN source_icon TEXT")
	_, _ = db.Exec("UPDATE seen_news SET source_icon = '' WHERE source_icon IS NULL")
	_, _ = db.Exec("ALTER TABLE seen_news ADD COLUMN link_normalized TEXT")
	_, _ = db.Exec("ALTER TABLE seen_news ADD COLUMN dedup_key TEXT")
	_, _ = db.Exec("UPDATE seen_news SET link_normalized = '' WHERE link_normalized IS NULL")
	_, _ = db.Exec("UPDATE seen_news SET dedup_key = '' WHERE dedup_key IS NULL")

	d := &Database{db: db}
	if err := d.backfillDedupColumns(); err != nil {
		return nil, err
	}

	return d, nil
}

func sourceBucketExpr() string {
	return `COALESCE(NULLIF(TRIM(source), ''), '')`
}

func (d *Database) backfillDedupColumns() error {
	rows, err := d.db.Query(`SELECT guid, COALESCE(link, ''), COALESCE(source, ''), COALESCE(title, '') FROM seen_news
		WHERE TRIM(COALESCE(dedup_key, '')) = ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var guid, link, src, title string
		if err := rows.Scan(&guid, &link, &src, &title); err != nil {
			return err
		}
		norm := NormalizeRSSLink(link)
		dedup := DedupKeyFromParts(src, norm, guid, title)
		_, err := d.db.Exec(`UPDATE seen_news SET link_normalized = ?, dedup_key = ? WHERE guid = ?`,
			norm, dedup, guid)
		if err != nil {
			return err
		}
	}
	return rows.Err()
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

// NewsItemDuplicate reports whether this item is already stored (by row id, dedup hash, or normalized link).
func (d *Database) NewsItemDuplicate(guid, dedupKey, linkNormalized string) (bool, error) {
	guid = strings.TrimSpace(guid)
	dedupKey = strings.TrimSpace(dedupKey)
	linkNormalized = strings.TrimSpace(linkNormalized)
	var exists bool
	err := d.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM seen_news WHERE guid = ?
			OR (TRIM(COALESCE(dedup_key, '')) != '' AND dedup_key = ?)
			OR (TRIM(COALESCE(link_normalized, '')) != '' AND link_normalized = ?)
		)`, guid, dedupKey, linkNormalized).Scan(&exists)
	return exists, err
}

func (d *Database) MarkSeen(entry NewsEntry) error {
	if entry.GUID == "" {
		return fmt.Errorf("cannot mark news as seen with empty GUID")
	}
	if strings.TrimSpace(entry.DedupKey) == "" {
		return fmt.Errorf("cannot mark news as seen with empty dedup key")
	}
	_, err := d.db.Exec(`INSERT INTO seen_news (guid, title, link, short_link, pub_date, source, source_icon, link_normalized, dedup_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.GUID, entry.Title, entry.Link, entry.ShortLink, entry.PubDate, entry.Source, entry.SourceIcon, entry.LinkNormalized, entry.DedupKey)
	return err
}

func (d *Database) GetLastNews(limit int) ([]NewsEntry, error) {
	rows, err := d.db.Query("SELECT guid, title, COALESCE(link, ''), COALESCE(short_link, ''), pub_date, COALESCE(source, ''), COALESCE(source_icon, '') FROM seen_news ORDER BY added_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []NewsEntry
	for rows.Next() {
		var e NewsEntry
		if err := rows.Scan(&e.GUID, &e.Title, &e.Link, &e.ShortLink, &e.PubDate, &e.Source, &e.SourceIcon); err != nil {
			return nil, err
		}
		e.Source = strings.TrimSpace(e.Source)
		e.SourceIcon = strings.TrimSpace(e.SourceIcon)
		entries = append(entries, e)
	}
	return entries, nil
}

func (d *Database) GetNews(limit, offset int, query string) ([]NewsEntry, int, error) {
	var total int
	err := d.db.QueryRow("SELECT COUNT(*) FROM seen_news WHERE title LIKE ?", "%"+query+"%").Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := d.db.Query(`
		SELECT guid, title, COALESCE(link, ''), COALESCE(short_link, ''), pub_date, COALESCE(source, ''), COALESCE(source_icon, '')
		FROM seen_news 
		WHERE title LIKE ? 
		ORDER BY added_at DESC 
		LIMIT ? OFFSET ?`, 
		"%"+query+"%", limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entries []NewsEntry
	for rows.Next() {
		var e NewsEntry
		if err := rows.Scan(&e.GUID, &e.Title, &e.Link, &e.ShortLink, &e.PubDate, &e.Source, &e.SourceIcon); err != nil {
			return nil, 0, err
		}
		e.Source = strings.TrimSpace(e.Source)
		e.SourceIcon = strings.TrimSpace(e.SourceIcon)
		entries = append(entries, e)
	}
	return entries, total, nil
}

// CountSeenNews returns how many GUID rows are stored in seen_news.
func (d *Database) CountSeenNews() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM seen_news`).Scan(&n)
	return n, err
}

func (d *Database) DeleteEntry(guid string) error {
	_, err := d.db.Exec("DELETE FROM seen_news WHERE guid = ?", guid)
	return err
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

// CleanupPerSource retains the newest keepPerSource rows per source bucket (empty source groups legacy rows together).
func (d *Database) CleanupPerSource(keepPerSource int) error {
	if keepPerSource <= 0 {
		return nil
	}
	rows, err := d.db.Query(`SELECT DISTINCT ` + sourceBucketExpr() + ` AS src FROM seen_news`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var buckets []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		buckets = append(buckets, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, src := range buckets {
		_, err := d.db.Exec(`
			DELETE FROM seen_news WHERE rowid IN (
				SELECT rowid FROM seen_news
				WHERE `+sourceBucketExpr()+` = ?
				ORDER BY datetime(added_at) DESC
				LIMIT -1 OFFSET ?
			)`, src, keepPerSource)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) DropAll() error {
	_, err := d.db.Exec("DELETE FROM seen_news")
	return err
}

func (d *Database) Close() error {
	return d.db.Close()
}
