package rss

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"botIAask/db"
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
	sqldb, err := db.OpenDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	_, err = sqldb.Exec(`
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
	_, _ = sqldb.Exec("ALTER TABLE seen_news ADD COLUMN link TEXT")
	_, _ = sqldb.Exec("ALTER TABLE seen_news ADD COLUMN short_link TEXT")
	_, _ = sqldb.Exec("ALTER TABLE seen_news ADD COLUMN source TEXT")
	// Fix NULL links from migration
	_, _ = sqldb.Exec("UPDATE seen_news SET link = '' WHERE link IS NULL")
	_, _ = sqldb.Exec("UPDATE seen_news SET source = '' WHERE source IS NULL")
	_, _ = sqldb.Exec("ALTER TABLE seen_news ADD COLUMN source_icon TEXT")
	_, _ = sqldb.Exec("UPDATE seen_news SET source_icon = '' WHERE source_icon IS NULL")
	_, _ = sqldb.Exec("ALTER TABLE seen_news ADD COLUMN link_normalized TEXT")
	_, _ = sqldb.Exec("ALTER TABLE seen_news ADD COLUMN dedup_key TEXT")
	_, _ = sqldb.Exec("UPDATE seen_news SET link_normalized = '' WHERE link_normalized IS NULL")
	_, _ = sqldb.Exec("UPDATE seen_news SET dedup_key = '' WHERE dedup_key IS NULL")
	// last_seen: refreshed whenever an item is still present in a live feed, so pruning
	// never drops rows the feed would immediately re-offer as "new".
	_, _ = sqldb.Exec("ALTER TABLE seen_news ADD COLUMN last_seen DATETIME")

	d := &Database{db: sqldb}
	if err := d.backfillDedupColumns(); err != nil {
		return nil, err
	}
	// Created after backfill so the column values the indexes cover are already populated.
	for _, ddl := range []string{
		"CREATE INDEX IF NOT EXISTS idx_seen_news_dedup_key ON seen_news(dedup_key)",
		"CREATE INDEX IF NOT EXISTS idx_seen_news_link_norm ON seen_news(link_normalized)",
	} {
		if _, err := sqldb.Exec(ddl); err != nil {
			return nil, fmt.Errorf("failed to create index: %w", err)
		}
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
	type row struct {
		guid, link, src, title string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.guid, &r.link, &r.src, &r.title); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range pending {
		norm := NormalizeRSSLink(r.link)
		dedup := DedupKeyFromParts(r.src, norm, r.guid, r.title)
		if _, err := d.db.Exec(`UPDATE seen_news SET link_normalized = ?, dedup_key = ? WHERE guid = ?`,
			norm, dedup, r.guid); err != nil {
			return err
		}
	}
	return nil
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
// It also stamps last_seen on the matching row, so one indexed query both checks and marks the item as
// still present in the live feed (see CleanupPerSource).
func (d *Database) NewsItemDuplicate(guid, dedupKey, linkNormalized string) (bool, error) {
	guid = strings.TrimSpace(guid)
	dedupKey = strings.TrimSpace(dedupKey)
	linkNormalized = strings.TrimSpace(linkNormalized)
	// NULLIF: an empty key must never match (NULL = anything is not true).
	res, err := d.db.Exec(`
		UPDATE seen_news SET last_seen = CURRENT_TIMESTAMP
		WHERE guid = ? OR dedup_key = NULLIF(?, '') OR link_normalized = NULLIF(?, '')`,
		guid, dedupKey, linkNormalized)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (d *Database) MarkSeen(entry NewsEntry) error {
	if entry.GUID == "" {
		return fmt.Errorf("cannot mark news as seen with empty GUID")
	}
	if strings.TrimSpace(entry.DedupKey) == "" {
		return fmt.Errorf("cannot mark news as seen with empty dedup key")
	}
	_, err := d.db.Exec(`INSERT INTO seen_news (guid, title, link, short_link, pub_date, source, source_icon, link_normalized, dedup_key, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
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

// CleanupPerSource retains the newest keepPerSource rows per source bucket (empty source groups legacy rows together).
// Rows seen in a live feed within the last 2 days are never deleted, even beyond the cap: a feed longer than
// keepPerSource would otherwise have its tail pruned and re-announced as new on the next cycle.
// Rows seen in a live feed within the last 2 days are never deleted, even beyond the cap: a feed longer than
// keepPerSource would otherwise have its tail pruned and re-announced as new on the next cycle.
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
			) AND COALESCE(last_seen, added_at) < datetime('now', '-2 days')`, src, keepPerSource)
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
