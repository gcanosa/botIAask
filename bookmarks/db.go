package bookmarks

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
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

// Reminder is a user-owned note keyed by a short public_id (hex, like paste tickets).
type Reminder struct {
	PublicID  string    `json:"public_id"`
	OwnerNick string    `json:"owner_nick"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open bookmarks database: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 8000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite busy_timeout: %w", err)
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
		db.Close()
		return nil, fmt.Errorf("failed to create bookmarks table: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS reminders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			public_id TEXT NOT NULL UNIQUE,
			owner_nick TEXT NOT NULL,
			note TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create reminders table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_reminders_owner_nick ON reminders (owner_nick)`)

	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")

	return &Database{db: db}, nil
}

func (d *Database) AddBookmark(url, nickname, hostname string) (int64, error) {
	res, err := d.db.Exec("INSERT INTO bookmarks (url, nickname, hostname) VALUES (?, ?, ?)",
		url, nickname, hostname)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CountReminders returns the total number of stored reminders.
func (d *Database) CountReminders() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM reminders`).Scan(&n)
	return n, err
}

func (d *Database) GetBookmarksCount(query string) (int, error) {
	var count int
	var err error
	if query == "" {
		err = d.db.QueryRow("SELECT COUNT(*) FROM bookmarks").Scan(&count)
	} else {
		q := "%" + query + "%"
		err = d.db.QueryRow(`
			SELECT COUNT(*) FROM bookmarks 
			WHERE id = ? OR url LIKE ? OR nickname LIKE ? OR hostname LIKE ?
		`, query, q, q, q).Scan(&count)
	}
	return count, err
}

func (d *Database) GetBookmarks(limit, offset int, query string) ([]Bookmark, error) {
	var rows *sql.Rows
	var err error

	if query == "" {
		rows, err = d.db.Query("SELECT id, url, nickname, hostname, timestamp FROM bookmarks ORDER BY timestamp DESC LIMIT ? OFFSET ?", limit, offset)
	} else {
		q := "%" + query + "%"
		rows, err = d.db.Query(`
			SELECT id, url, nickname, hostname, timestamp FROM bookmarks 
			WHERE id = ? OR url LIKE ? OR nickname LIKE ? OR hostname LIKE ?
			ORDER BY timestamp DESC LIMIT ? OFFSET ?
		`, query, q, q, q, limit, offset)
	}

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

// FindBookmarksByURLContains returns bookmarks whose URL contains pattern (substring match, newest first).
func (d *Database) FindBookmarksByURLContains(pattern string, limit int) ([]Bookmark, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	pat := bookmarkURLLikePattern(pattern)
	rows, err := d.db.Query(`
		SELECT id, url, nickname, hostname, timestamp FROM bookmarks
		WHERE url LIKE ? ESCAPE '\'
		ORDER BY timestamp DESC LIMIT ?`, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.URL, &b.Nickname, &b.Hostname, &b.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (d *Database) CountUserBookmarksSince(nickname string, since time.Time) (int, error) {
	var count int
	err := d.db.QueryRow("SELECT COUNT(*) FROM bookmarks WHERE nickname = ? AND timestamp > ?", 
		nickname, since).Scan(&count)
	return count, err
}

func (d *Database) DeleteBookmark(id int) error {
	res, err := d.db.Exec("DELETE FROM bookmarks WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("bookmark not found")
	}
	return nil
}

func generateReminderPublicID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IRCCaseFoldNick applies RFC 1459 ASCII case mapping for IRC nick comparison ([→{, ]→}, etc.).
func IRCCaseFoldNick(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '^':
			b.WriteRune('~')
		case '\\':
			b.WriteRune('|')
		case '[':
			b.WriteRune('{')
		case ']':
			b.WriteRune('}')
		default:
			if r >= 'A' && r <= 'Z' {
				b.WriteRune(r + ('a' - 'A'))
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// AddReminder inserts a reminder; returns the new public_id (8 hex chars, paste-ticket style).
func (d *Database) AddReminder(ownerNick, note string) (string, error) {
	ownerNick = strings.TrimSpace(ownerNick)
	note = strings.TrimSpace(note)
	if ownerNick == "" || note == "" {
		return "", fmt.Errorf("owner and note required")
	}
	for range 8 {
		id, err := generateReminderPublicID()
		if err != nil {
			return "", err
		}
		_, err = d.db.Exec(
			`INSERT INTO reminders (public_id, owner_nick, note, created_at) VALUES (?, ?, ?, ?)`,
			id, ownerNick, note, time.Now(),
		)
		if err == nil {
			return id, nil
		}
		if strings.Contains(err.Error(), "UNIQUE") {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("could not allocate unique reminder id")
}

// DeleteReminder removes a reminder if public_id exists and owner matches (IRC case fold).
func (d *Database) DeleteReminder(ownerNick, publicID string) (bool, error) {
	publicID = strings.TrimSpace(publicID)
	if publicID == "" {
		return false, nil
	}
	var storedOwner string
	err := d.db.QueryRow(`SELECT owner_nick FROM reminders WHERE public_id = ?`, publicID).Scan(&storedOwner)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if IRCCaseFoldNick(strings.TrimSpace(storedOwner)) != IRCCaseFoldNick(strings.TrimSpace(ownerNick)) {
		return false, nil
	}
	res, err := d.db.Exec(`DELETE FROM reminders WHERE public_id = ?`, publicID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListReminders returns reminders for ownerNick, ordered by created_at ascending.
func (d *Database) ListReminders(ownerNick string) ([]Reminder, error) {
	uname := strings.TrimSpace(ownerNick)
	targetFold := IRCCaseFoldNick(uname)

	rows, err := d.db.Query(`
		SELECT public_id, owner_nick, note, created_at FROM reminders
		WHERE LOWER(TRIM(owner_nick)) = LOWER(?)
		ORDER BY created_at ASC`, uname)
	if err != nil {
		return nil, err
	}
	list, err := scanReminderRowsFiltered(rows, targetFold)
	if err != nil {
		return nil, err
	}
	if len(list) > 0 {
		return list, nil
	}

	rows2, err := d.db.Query(`
		SELECT public_id, owner_nick, note, created_at FROM reminders
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	var out []Reminder
	for rows2.Next() {
		var r Reminder
		if err := rows2.Scan(&r.PublicID, &r.OwnerNick, &r.Note, &r.CreatedAt); err != nil {
			return nil, err
		}
		if IRCCaseFoldNick(strings.TrimSpace(r.OwnerNick)) == targetFold {
			out = append(out, r)
		}
	}
	return out, rows2.Err()
}

func scanReminderRowsFiltered(rows *sql.Rows, targetFold string) ([]Reminder, error) {
	defer rows.Close()
	var list []Reminder
	for rows.Next() {
		var r Reminder
		if err := rows.Scan(&r.PublicID, &r.OwnerNick, &r.Note, &r.CreatedAt); err != nil {
			return nil, err
		}
		if IRCCaseFoldNick(strings.TrimSpace(r.OwnerNick)) == targetFold {
			list = append(list, r)
		}
	}
	return list, rows.Err()
}

func (d *Database) Close() error {
	return d.db.Close()
}
