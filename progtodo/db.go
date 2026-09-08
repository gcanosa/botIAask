// Package progtodo stores programmer TODO / feature suggestions (IRC + web).
package progtodo

import (
	"botIAask/db"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Entry is one backlog item.
type Entry struct {
	ID         string    `json:"id"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
	AuthorNick string    `json:"author_nick"`
	// Network is the IRC network the author was on (empty for legacy rows predating
	// multi-network support, and for the reserved "web" sentinel — see Add).
	Network      string `json:"network"`
	AdminOnly    bool   `json:"admin_only"`
	Importance   string `json:"importance"`
	ReviewStatus string `json:"review_status"`
	Disabled     bool   `json:"disabled"`
}

// WebNetwork is the reserved Network value for entries created from the web dashboard, so a
// dashboard username can never collide with (and be deleted/listed via) an IRC nick sharing
// the same text on some connected network.
const WebNetwork = "web"

// Database is programmer todos SQLite storage.
type Database struct {
	db *sql.DB
}

// NewDatabase opens or creates the DB at dbPath.
func NewDatabase(dbPath string) (*Database, error) {
	sqldb, err := db.OpenDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("progtodo: open: %w", err)
	}
	_, err = sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS programmer_todos (
			id TEXT PRIMARY KEY,
			body TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			author_nick TEXT NOT NULL,
			admin_only INTEGER NOT NULL DEFAULT 0,
			importance TEXT NOT NULL DEFAULT 'medium',
			review_status TEXT NOT NULL DEFAULT 'pending',
			disabled INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("progtodo: create table: %w", err)
	}
	_, _ = sqldb.Exec(`CREATE INDEX IF NOT EXISTS idx_progtodo_author ON programmer_todos (author_nick)`)
	_, _ = sqldb.Exec(`CREATE INDEX IF NOT EXISTS idx_progtodo_admin_only ON programmer_todos (admin_only)`)
	// Migration: network (IRC network the author was on), for multi-network scoping so the
	// same nick on two networks can't list/delete each other's TODOs. Project convention:
	// tolerate the "duplicate column" error on re-runs (ALTER TABLE ADD COLUMN can't be
	// guarded by IF NOT EXISTS in SQLite).
	if _, err := sqldb.Exec(`ALTER TABLE programmer_todos ADD COLUMN network TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		sqldb.Close()
		return nil, fmt.Errorf("progtodo: add network column: %w", err)
	}
	_, _ = sqldb.Exec(`CREATE INDEX IF NOT EXISTS idx_progtodo_network_author ON programmer_todos (network, author_nick)`)
	if err := runProgtodoMigrations(sqldb); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("progtodo: migrate: %w", err)
	}
	return &Database{db: sqldb}, nil
}

// runProgtodoMigrations applies one-time PRAGMA user_version upgrades.
// v2: Old IRC behavior set admin_only=1 for every !todo add from config admins, hiding the whole backlog
// from non-staff on the web. We once clear that flag; staff-only going forward is !todo private / !todo staff.
func runProgtodoMigrations(db *sql.DB) error {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= 2 {
		return nil
	}
	if _, err := db.Exec(`UPDATE programmer_todos SET admin_only = 0`); err != nil {
		return err
	}
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		return err
	}
	return nil
}

// Close releases the database handle.
func (d *Database) Close() error { return d.db.Close() }

// BackfillLegacyNetwork attributes rows left with network = ” (created before the network
// column existed) to defaultNetwork. Callers must only invoke this when the resolved default
// is unambiguous — i.e. exactly one IRC network is configured; main.go enforces that gate.
// No per-network uniqueness constraint exists on this table, so a plain update is sufficient.
func (d *Database) BackfillLegacyNetwork(defaultNetwork string) error {
	defaultNetwork = strings.TrimSpace(defaultNetwork)
	if defaultNetwork == "" {
		return fmt.Errorf("progtodo: BackfillLegacyNetwork: empty defaultNetwork")
	}
	_, err := d.db.Exec(`UPDATE programmer_todos SET network = ? WHERE network = ''`, defaultNetwork)
	return err
}

func newID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Add creates a new entry; returns the public id (8 hex chars, same style as upload ticket ids).
// network scopes ownership so the same nick on two IRC networks (or the WebNetwork sentinel
// for dashboard-created entries) can't see or delete each other's rows — see ListByAuthor,
// DeleteByAuthor. importance: empty or unknown defaults to "medium"; must be low, medium, or high.
func (d *Database) Add(body, authorNick, network string, adminOnly bool, importance string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("empty body")
	}
	authorNick = strings.TrimSpace(authorNick)
	if authorNick == "" {
		return "", fmt.Errorf("empty author")
	}
	imp := strings.ToLower(strings.TrimSpace(importance))
	if imp == "" {
		imp = "medium"
	}
	if imp != "low" && imp != "medium" && imp != "high" {
		return "", fmt.Errorf("invalid importance")
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	ao := 0
	if adminOnly {
		ao = 1
	}
	_, err = d.db.Exec(`
		INSERT INTO programmer_todos (id, body, author_nick, network, admin_only, importance, review_status, disabled)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', 0)`,
		id, body, authorNick, network, ao, imp)
	if err != nil {
		return "", err
	}
	return id, nil
}

// ListByAuthor returns todos created by the given nick on the given network (for IRC !todo list).
func (d *Database) ListByAuthor(authorNick, network string) ([]Entry, error) {
	authorNick = strings.TrimSpace(authorNick)
	rows, err := d.db.Query(`
		SELECT id, body, created_at, author_nick, network, admin_only, importance, review_status, disabled
		FROM programmer_todos
		WHERE author_nick = ? AND network = ?
		ORDER BY datetime(created_at) DESC`, authorNick, network)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// DeleteByAuthor deletes a row only if it belongs to authorNick on network.
func (d *Database) DeleteByAuthor(authorNick, network, publicID string) (bool, error) {
	authorNick, publicID = strings.TrimSpace(authorNick), strings.TrimSpace(publicID)
	if publicID == "" {
		return false, nil
	}
	res, err := d.db.Exec(`DELETE FROM programmer_todos WHERE id = ? AND author_nick = ? AND network = ?`, publicID, authorNick, network)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListPublic returns non–admin-only entries for anonymous / non-staff web viewers
// (including rejected rows; UI shows strikethrough for rejected).
func (d *Database) ListPublic() ([]Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, body, created_at, author_nick, network, admin_only, importance, review_status, disabled
		FROM programmer_todos
		WHERE COALESCE(admin_only, 0) = 0
		ORDER BY
			CASE importance WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END,
			datetime(created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// ListAll returns every row (staff).
func (d *Database) ListAll() ([]Entry, error) {
	rows, err := d.db.Query(`
		SELECT id, body, created_at, author_nick, network, admin_only, importance, review_status, disabled
		FROM programmer_todos
		ORDER BY
			CASE importance WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END,
			datetime(created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// GetByID returns a single entry or nil if not found.
func (d *Database) GetByID(id string) (*Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("missing id")
	}
	row := d.db.QueryRow(`
		SELECT id, body, created_at, author_nick, network, admin_only, importance, review_status, disabled
		FROM programmer_todos WHERE id = ?`, id)
	var e Entry
	var adminOnly, disabled int
	var created string
	err := row.Scan(&e.ID, &e.Body, &created, &e.AuthorNick, &e.Network, &adminOnly, &e.Importance, &e.ReviewStatus, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.AdminOnly = adminOnly != 0
	e.Disabled = disabled != 0
	e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	if e.CreatedAt.IsZero() {
		e.CreatedAt, _ = time.Parse(time.RFC3339, created)
	}
	return &e, nil
}

// UpdateStaff sets importance and/or review_status. Once approved or rejected, review_status cannot change.
// Rejected rows get disabled=1 (strikethrough); approved/pending get disabled=0.
func (d *Database) UpdateStaff(id string, importance, reviewStatus string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing id")
	}
	cur, err := d.GetByID(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("entry not found")
	}
	curRev := strings.ToLower(strings.TrimSpace(cur.ReviewStatus))
	if curRev == "" {
		curRev = "pending"
	}

	imp := strings.ToLower(strings.TrimSpace(importance))
	if imp != "" && imp != "low" && imp != "medium" && imp != "high" {
		return fmt.Errorf("invalid importance")
	}
	st := strings.ToLower(strings.TrimSpace(reviewStatus))
	if st != "" && st != "pending" && st != "approved" && st != "rejected" {
		return fmt.Errorf("invalid review_status")
	}

	if st != "" {
		if curRev == "approved" && st != "approved" {
			return fmt.Errorf("cannot change review: already approved")
		}
		if curRev == "rejected" && st != "rejected" {
			return fmt.Errorf("cannot change review: already rejected")
		}
	}

	var sets []string
	var args []interface{}
	if imp != "" {
		sets = append(sets, "importance = ?")
		args = append(args, imp)
	}
	if st != "" {
		sets = append(sets, "review_status = ?")
		args = append(args, st)
		// Strikethrough in UI is driven by "rejected"; keep disabled in sync.
		if st == "rejected" {
			sets = append(sets, "disabled = 1")
		} else {
			sets = append(sets, "disabled = 0")
		}
	}
	if len(sets) == 0 {
		return fmt.Errorf("no fields to update")
	}
	args = append(args, id)
	q := "UPDATE programmer_todos SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	_, err = d.db.Exec(q, args...)
	return err
}

// DeleteByID deletes any row (staff).
func (d *Database) DeleteByID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing id")
	}
	_, err := d.db.Exec(`DELETE FROM programmer_todos WHERE id = ?`, id)
	return err
}

func scanEntries(rows *sql.Rows) ([]Entry, error) {
	var out []Entry
	for rows.Next() {
		var e Entry
		var adminOnly, disabled int
		var created string
		err := rows.Scan(&e.ID, &e.Body, &created, &e.AuthorNick, &e.Network, &adminOnly, &e.Importance, &e.ReviewStatus, &disabled)
		if err != nil {
			return nil, err
		}
		e.AdminOnly = adminOnly != 0
		e.Disabled = disabled != 0
		e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		if e.CreatedAt.IsZero() {
			e.CreatedAt, _ = time.Parse(time.RFC3339, created)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
