package github

import (
	"database/sql"
	"fmt"
	"time"

	"botIAask/db"
)

// Database is GitHub tracker SQLite storage: which events have already been announced
// (dedup) and the last ETag seen per repo (conditional GET, saves rate-limit budget).
type Database struct {
	db *sql.DB
}

// NewDatabase opens or creates the DB at dbPath.
func NewDatabase(dbPath string) (*Database, error) {
	sqldb, err := db.OpenDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("github: open: %w", err)
	}
	_, err = sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS seen_events (
			event_key TEXT PRIMARY KEY,
			repo TEXT NOT NULL,
			kind TEXT NOT NULL,
			occurred_at DATETIME,
			added_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("github: create seen_events: %w", err)
	}
	_, _ = sqldb.Exec(`CREATE INDEX IF NOT EXISTS idx_github_seen_repo ON seen_events (repo)`)

	_, err = sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS repo_etags (
			repo TEXT PRIMARY KEY,
			etag TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("github: create repo_etags: %w", err)
	}

	return &Database{db: sqldb}, nil
}

// EventKey builds the stable dedup key for a repo+event ID pair.
func EventKey(owner, repo, githubEventID string) string {
	return owner + "/" + repo + "#" + githubEventID
}

// EventSeen reports whether eventKey has already been recorded.
func (d *Database) EventSeen(eventKey string) (bool, error) {
	var exists int
	err := d.db.QueryRow(`SELECT 1 FROM seen_events WHERE event_key = ?`, eventKey).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkEventSeen records eventKey as announced. INSERT OR IGNORE tolerates a race between
// overlapping Fetch() calls (e.g. a slow poll cycle still running when the next ticks)
// without erroring.
func (d *Database) MarkEventSeen(eventKey, repo, kind string, occurredAt time.Time) error {
	_, err := d.db.Exec(
		`INSERT OR IGNORE INTO seen_events (event_key, repo, kind, occurred_at) VALUES (?, ?, ?, ?)`,
		eventKey, repo, kind, occurredAt,
	)
	return err
}

// GetETag returns the stored ETag for repo, or "" if none is stored yet.
func (d *Database) GetETag(repo string) (string, error) {
	var etag string
	err := d.db.QueryRow(`SELECT etag FROM repo_etags WHERE repo = ?`, repo).Scan(&etag)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return etag, nil
}

// SetETag upserts the ETag for repo.
func (d *Database) SetETag(repo, etag string) error {
	_, err := d.db.Exec(`
		INSERT INTO repo_etags (repo, etag, updated_at) VALUES (?, ?, datetime('now'))
		ON CONFLICT(repo) DO UPDATE SET etag = excluded.etag, updated_at = excluded.updated_at
	`, repo, etag)
	return err
}

// CleanupOlderThan deletes seen_events rows older than the given number of days, mirroring
// GitHub's own ~90-day event-visibility window so the table doesn't grow unbounded.
func (d *Database) CleanupOlderThan(days int) error {
	_, err := d.db.Exec(`DELETE FROM seen_events WHERE added_at < datetime('now', ?)`, fmt.Sprintf("-%d days", days))
	return err
}

// Close closes the underlying database connection.
func (d *Database) Close() error {
	return d.db.Close()
}
