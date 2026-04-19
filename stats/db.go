package stats

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Database struct {
	db *sql.DB
}

type StatEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	Messages    int       `json:"messages"`
	Actions     int       `json:"actions"`
	AIRequests  int       `json:"ai_requests"`
	UserCount   int       `json:"user_count"`
	Joins       int       `json:"joins"`
	Parts       int       `json:"parts"`
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open stats database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS bot_stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			messages INTEGER DEFAULT 0,
			actions INTEGER DEFAULT 0,
			ai_requests INTEGER DEFAULT 0,
			user_count INTEGER DEFAULT 0,
			joins INTEGER DEFAULT 0,
			parts INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_stats_timestamp ON bot_stats(timestamp);
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create stats table: %w", err)
	}

	return &Database{db: db}, nil
}

func (d *Database) SaveEntry(e StatEntry) error {
	_, err := d.db.Exec(`
		INSERT INTO bot_stats (timestamp, messages, actions, ai_requests, user_count, joins, parts)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, e.Timestamp, e.Messages, e.Actions, e.AIRequests, e.UserCount, e.Joins, e.Parts)
	return err
}

func (d *Database) GetRecentStats(limit int) ([]StatEntry, error) {
	rows, err := d.db.Query(`
		SELECT timestamp, messages, actions, ai_requests, user_count, joins, parts
		FROM bot_stats
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []StatEntry
	for rows.Next() {
		var e StatEntry
		if err := rows.Scan(&e.Timestamp, &e.Messages, &e.Actions, &e.AIRequests, &e.UserCount, &e.Joins, &e.Parts); err != nil {
			return nil, err
		}
		// Prepend to maintain chronological order in the result slice
		entries = append([]StatEntry{e}, entries...)
	}
	return entries, nil
}

func (d *Database) GetStatsSince(since time.Time) ([]StatEntry, error) {
	rows, err := d.db.Query(`
		SELECT timestamp, messages, actions, ai_requests, user_count, joins, parts
		FROM bot_stats
		WHERE timestamp >= ?
		ORDER BY timestamp ASC
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []StatEntry
	for rows.Next() {
		var e StatEntry
		if err := rows.Scan(&e.Timestamp, &e.Messages, &e.Actions, &e.AIRequests, &e.UserCount, &e.Joins, &e.Parts); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (d *Database) Cleanup(days int) error {
	if days <= 0 {
		return nil
	}
	threshold := time.Now().AddDate(0, 0, -days)
	_, err := d.db.Exec("DELETE FROM bot_stats WHERE timestamp < ?", threshold)
	return err
}

func (d *Database) Close() error {
	return d.db.Close()
}
