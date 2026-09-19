package stats

import (
	"botIAask/db"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Database struct {
	db *sql.DB
}

type StatEntry struct {
	Timestamp time.Time `json:"timestamp"`
	// Network is the IRC network this row's activity happened on. Empty in two cases:
	// rows predating multi-network stats, and an aggregate row returned by GetRecentStats/
	// GetStatsSince when called with no network filter (SUMmed across every network sharing
	// that timestamp) — the default the dashboard's activity chart shows.
	Network        string              `json:"network,omitempty"`
	Messages       int                 `json:"messages"`
	Actions        int                 `json:"actions"`
	AIRequests     int                 `json:"ai_requests"`
	UserCount      int                 `json:"user_count"`
	Joins          int                 `json:"joins"`
	Parts          int                 `json:"parts"`
	AdminCommands  int                 `json:"admin_commands"`
	LoggedInAdmins int                 `json:"logged_in_admins"`
	FailedAuths    int                 `json:"failed_auths"`
	AdminNicknames []string            `json:"admin_nicknames,omitempty"`
	ChannelAdmins  map[string][]string `json:"channel_admins,omitempty"`
}

func NewDatabase(dbPath string) (*Database, error) {
	sqldb, err := db.OpenDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open stats database: %w", err)
	}

	_, err = sqldb.Exec(`
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

	if err := migrateStatsSchema(sqldb); err != nil {
		return nil, err
	}

	if err := createChanActivity(sqldb); err != nil {
		return nil, err
	}

	return &Database{db: sqldb}, nil
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
		// network: which IRC network this row's activity belongs to, for multi-network
		// attribution. Existing rows default to '' (pre-multi-network history; treated as
		// its own bucket, folded into the aggregate view by GetStatsSince/GetRecentStats).
		{`ALTER TABLE bot_stats ADD COLUMN network TEXT NOT NULL DEFAULT ''`},
	}
	labels := []string{"admin_commands", "logged_in_admins", "failed_auths", "network"}
	for i, a := range adds {
		if _, ok := have[labels[i]]; ok {
			continue
		}
		if _, err := db.Exec(a.sql); err != nil {
			return fmt.Errorf("stats migrate add %s: %w", labels[i], err)
		}
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_stats_network_timestamp ON bot_stats(network, timestamp)`)
	return nil
}

func (d *Database) SaveEntry(e StatEntry) error {
	_, err := d.db.Exec(`
		INSERT INTO bot_stats (timestamp, network, messages, actions, ai_requests, user_count, joins, parts, admin_commands, logged_in_admins, failed_auths)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, e.Timestamp, e.Network, e.Messages, e.Actions, e.AIRequests, e.UserCount, e.Joins, e.Parts, e.AdminCommands, e.LoggedInAdmins, e.FailedAuths)
	return err
}

// GetRecentStats returns the most recent limit rows, chronological order. When network is
// empty, rows sharing a timestamp are summed across every network (the aggregate the
// dashboard's activity chart shows by default); pass a network name to see just its history.
func (d *Database) GetRecentStats(limit int, network string) ([]StatEntry, error) {
	var rows *sql.Rows
	var err error
	if network == "" {
		rows, err = d.db.Query(`
			SELECT timestamp, SUM(messages), SUM(actions), SUM(ai_requests), SUM(user_count), SUM(joins), SUM(parts), SUM(admin_commands), SUM(logged_in_admins), SUM(failed_auths)
			FROM bot_stats
			GROUP BY timestamp
			ORDER BY timestamp DESC
			LIMIT ?
		`, limit)
	} else {
		rows, err = d.db.Query(`
			SELECT timestamp, messages, actions, ai_requests, user_count, joins, parts, admin_commands, logged_in_admins, failed_auths
			FROM bot_stats
			WHERE network = ?
			ORDER BY timestamp DESC
			LIMIT ?
		`, network, limit)
	}
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
		e.Network = network
		entries = append(entries, e)
	}
	// Query is DESC (newest first); reverse once to return chronological order.
	slices.Reverse(entries)
	return entries, nil
}

// GetStatsSince returns rows at or after since, chronological order. When network is empty,
// rows sharing a timestamp are summed across every network (the dashboard's default view);
// pass a network name to see just its history.
func (d *Database) GetStatsSince(since time.Time, network string) ([]StatEntry, error) {
	var rows *sql.Rows
	var err error
	if network == "" {
		rows, err = d.db.Query(`
			SELECT timestamp, SUM(messages), SUM(actions), SUM(ai_requests), SUM(user_count), SUM(joins), SUM(parts), SUM(admin_commands), SUM(logged_in_admins), SUM(failed_auths)
			FROM bot_stats
			WHERE timestamp >= ?
			GROUP BY timestamp
			ORDER BY timestamp ASC
		`, since)
	} else {
		rows, err = d.db.Query(`
			SELECT timestamp, messages, actions, ai_requests, user_count, joins, parts, admin_commands, logged_in_admins, failed_auths
			FROM bot_stats
			WHERE timestamp >= ? AND network = ?
			ORDER BY timestamp ASC
		`, since, network)
	}
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
		e.Network = network
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
