// Package progtodo stores programmer TODO / feature suggestions (IRC + web).
package progtodo

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Entry is one backlog item.
type Entry struct {
	ID           string    `json:"id"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
	AuthorNick   string    `json:"author_nick"`
	AdminOnly    bool      `json:"admin_only"`
	Importance   string    `json:"importance"`
	ReviewStatus string    `json:"review_status"`
	Disabled     bool      `json:"disabled"`
}

// Database is programmer todos SQLite storage.
type Database struct {
	db *sql.DB
}

// NewDatabase opens or creates the DB at dbPath.
func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("progtodo: open: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 8000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("progtodo: busy_timeout: %w", err)
	}
	_, err = db.Exec(`
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
		db.Close()
		return nil, fmt.Errorf("progtodo: create table: %w", err)
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_progtodo_author ON programmer_todos (author_nick)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_progtodo_admin_only ON programmer_todos (admin_only)`)
	_, _ = db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = db.Exec("PRAGMA synchronous=NORMAL;")
	if err := runProgtodoMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("progtodo: migrate: %w", err)
	}
	return &Database{db: db}, nil
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

func newID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Add creates a new entry; returns the public id (8 hex chars, same style as upload ticket ids).
func (d *Database) Add(body, authorNick string, adminOnly bool) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("empty body")
	}
	authorNick = strings.TrimSpace(authorNick)
	if authorNick == "" {
		return "", fmt.Errorf("empty author")
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
		INSERT INTO programmer_todos (id, body, author_nick, admin_only, importance, review_status, disabled)
		VALUES (?, ?, ?, ?, 'medium', 'pending', 0)`,
		id, body, authorNick, ao)
	if err != nil {
		return "", err
	}
	return id, nil
}

// ListByAuthor returns all todos created by the given nick (for IRC !todo list).
func (d *Database) ListByAuthor(authorNick string) ([]Entry, error) {
	authorNick = strings.TrimSpace(authorNick)
	rows, err := d.db.Query(`
		SELECT id, body, created_at, author_nick, admin_only, importance, review_status, disabled
		FROM programmer_todos
		WHERE author_nick = ?
		ORDER BY datetime(created_at) DESC`, authorNick)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntries(rows)
}

// DeleteByAuthor deletes a row only if it belongs to authorNick.
func (d *Database) DeleteByAuthor(authorNick, publicID string) (bool, error) {
	authorNick, publicID = strings.TrimSpace(authorNick), strings.TrimSpace(publicID)
	if publicID == "" {
		return false, nil
	}
	res, err := d.db.Exec(`DELETE FROM programmer_todos WHERE id = ? AND author_nick = ?`, publicID, authorNick)
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
		SELECT id, body, created_at, author_nick, admin_only, importance, review_status, disabled
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
		SELECT id, body, created_at, author_nick, admin_only, importance, review_status, disabled
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
		SELECT id, body, created_at, author_nick, admin_only, importance, review_status, disabled
		FROM programmer_todos WHERE id = ?`, id)
	var e Entry
	var adminOnly, disabled int
	var created string
	err := row.Scan(&e.ID, &e.Body, &created, &e.AuthorNick, &adminOnly, &e.Importance, &e.ReviewStatus, &disabled)
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
		err := rows.Scan(&e.ID, &e.Body, &created, &e.AuthorNick, &adminOnly, &e.Importance, &e.ReviewStatus, &disabled)
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
