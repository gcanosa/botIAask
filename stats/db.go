package stats

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

type StatEntry struct {
	Timestamp        time.Time `json:"timestamp"`
	Messages         int       `json:"messages"`
	Actions          int       `json:"actions"`
	AIRequests       int       `json:"ai_requests"`
	UserCount        int       `json:"user_count"`
	Joins            int       `json:"joins"`
	Parts            int       `json:"parts"`
	AdminCommands    int       `json:"admin_commands"`
	LoggedInAdmins   int       `json:"logged_in_admins"`
	FailedAuths      int       `json:"failed_auths"`
	AdminNicknames   []string  `json:"admin_nicknames,omitempty"`
	ChannelAdmins    map[string][]string `json:"channel_admins,omitempty"`
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
			parts INTEGER DEFAULT 0,
			admin_commands INTEGER DEFAULT 0,
			logged_in_admins INTEGER DEFAULT 0,
			failed_auths INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_stats_timestamp ON bot_stats(timestamp);
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create stats table: %w", err)
	}

	if err := migrateStatsSchema(db); err != nil {
		return nil, err
	}

	return &Database{db: db}, nil
}

// migrateStatsSchema adds columns introduced after older deployments (CREATE TABLE IF NOT EXISTS does not upgrade schema).
func migrateStatsSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(bot_stats)`)
	if err != nil {
		return fmt.Errorf("stats schema inspect: %w", err)
	}
	defer rows.Close()

	have := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err = rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("stats schema scan: %w", err)
		}
		have[strings.ToLower(name)] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("stats schema rows: %w", err)
	}

	adds := []struct{ sql string }{
		{`ALTER TABLE bot_stats ADD COLUMN admin_commands INTEGER NOT NULL DEFAULT 0`},
		{`ALTER TABLE bot_stats ADD COLUMN logged_in_admins INTEGER NOT NULL DEFAULT 0`},
		{`ALTER TABLE bot_stats ADD COLUMN failed_auths INTEGER NOT NULL DEFAULT 0`},
	}
	labels := []string{"admin_commands", "logged_in_admins", "failed_auths"}
	for i, a := range adds {
		if _, ok := have[labels[i]]; ok {
			continue
		}
		if _, err := db.Exec(a.sql); err != nil {
			return fmt.Errorf("stats migrate add %s: %w", labels[i], err)
		}
	}
	return nil
}

func (d *Database) SaveEntry(e StatEntry) error {
	_, err := d.db.Exec(`
		INSERT INTO bot_stats (timestamp, messages, actions, ai_requests, user_count, joins, parts, admin_commands, logged_in_admins, failed_auths)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.Timestamp, e.Messages, e.Actions, e.AIRequests, e.UserCount, e.Joins, e.Parts, e.AdminCommands, e.LoggedInAdmins, e.FailedAuths)
	return err
}

func (d *Database) GetRecentStats(limit int) ([]StatEntry, error) {
	rows, err := d.db.Query(`
		SELECT timestamp, messages, actions, ai_requests, user_count, joins, parts, admin_commands, logged_in_admins, failed_auths
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
		if err := rows.Scan(&e.Timestamp, &e.Messages, &e.Actions, &e.AIRequests, &e.UserCount, &e.Joins, &e.Parts, &e.AdminCommands, &e.LoggedInAdmins, &e.FailedAuths); err != nil {
			return nil, err
		}
		// Prepend to maintain chronological order in the result slice
		entries = append([]StatEntry{e}, entries...)
	}
	return entries, nil
}

func (d *Database) GetStatsSince(since time.Time) ([]StatEntry, error) {
	rows, err := d.db.Query(`
		SELECT timestamp, messages, actions, ai_requests, user_count, joins, parts, admin_commands, logged_in_admins, failed_auths
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
		if err := rows.Scan(&e.Timestamp, &e.Messages, &e.Actions, &e.AIRequests, &e.UserCount, &e.Joins, &e.Parts, &e.AdminCommands, &e.LoggedInAdmins, &e.FailedAuths); err != nil {
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
