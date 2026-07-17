package bookmarks

import (
	"botIAask/db"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	
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
// DueAt nil means the legacy "deliver on next join" behavior; a set DueAt is a
// timed reminder the scheduler fires once at that time, then deletes.
type Reminder struct {
	PublicID  string     `json:"public_id"`
	OwnerNick string     `json:"owner_nick"`
	Note      string     `json:"note"`
	CreatedAt time.Time  `json:"created_at"`
	DueAt     *time.Time `json:"due_at,omitempty"`
}

// Tell is a message left for a nick, delivered when that nick is next seen.
type Tell struct {
	PublicID  string    `json:"public_id"`
	FromNick  string    `json:"from_nick"`
	ToNick    string    `json:"to_nick"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Seen records the last observed activity for a nick.
type Seen struct {
	Nick     string    `json:"nick"`
	Channel  string    `json:"channel"`
	Action   string    `json:"action"`
	Message  string    `json:"message"`
	LastSeen time.Time `json:"last_seen"`
}

func NewDatabase(dbPath string) (*Database, error) {
	sqldb, err := db.OpenDatabase( dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open bookmarks database: %w", err)
	}
	_, err = sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS bookmarks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			url TEXT UNIQUE,
			nickname TEXT,
			hostname TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("failed to create bookmarks table: %w", err)
	}

	_, err = sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS reminders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			public_id TEXT NOT NULL UNIQUE,
			owner_nick TEXT NOT NULL,
			note TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("failed to create reminders table: %w", err)
	}

	_, _ = sqldb.Exec(`CREATE INDEX IF NOT EXISTS idx_reminders_owner_nick ON reminders (owner_nick)`)

	// Migration: due_at for timed reminders (NULL = legacy on-join delivery).
	// Project convention: tolerate the "duplicate column" error on re-runs.
	if _, err := sqldb.Exec(`ALTER TABLE reminders ADD COLUMN due_at DATETIME`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		sqldb.Close()
		return nil, fmt.Errorf("failed to add reminders.due_at: %w", err)
	}

	if _, err = sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS tells (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			public_id TEXT NOT NULL UNIQUE,
			from_nick TEXT NOT NULL,
			to_fold TEXT NOT NULL,
			to_nick TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("failed to create tells table: %w", err)
	}
	_, _ = sqldb.Exec(`CREATE INDEX IF NOT EXISTS idx_tells_to_fold ON tells (to_fold)`)

	if _, err = sqldb.Exec(`
		CREATE TABLE IF NOT EXISTS seen (
			nick_fold TEXT PRIMARY KEY,
			nick TEXT NOT NULL,
			channel TEXT,
			action TEXT,
			message TEXT,
			last_seen DATETIME NOT NULL
		)
	`); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("failed to create seen table: %w", err)
	}

	return &Database{db: sqldb}, nil
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
// dueAt nil = legacy on-join delivery; a non-nil dueAt is fired once by the scheduler.
func (d *Database) AddReminder(ownerNick, note string, dueAt *time.Time) (string, error) {
	ownerNick = strings.TrimSpace(ownerNick)
	note = strings.TrimSpace(note)
	if ownerNick == "" || note == "" {
		return "", fmt.Errorf("owner and note required")
	}
	var due any
	if dueAt != nil {
		due = dueAt.UTC()
	}
	for range 8 {
		id, err := generateReminderPublicID()
		if err != nil {
			return "", err
		}
		_, err = d.db.Exec(
			`INSERT INTO reminders (public_id, owner_nick, note, created_at, due_at) VALUES (?, ?, ?, ?, ?)`,
			id, ownerNick, note, time.Now(), due,
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

// DueReminders returns timed reminders whose due time has passed, oldest first.
func (d *Database) DueReminders(now time.Time) ([]Reminder, error) {
	rows, err := d.db.Query(`
		SELECT public_id, owner_nick, note, created_at, due_at FROM reminders
		WHERE due_at IS NOT NULL AND due_at <= ?
		ORDER BY due_at ASC`, now.UTC())
	if err != nil {
		return nil, err
	}
	return scanReminders(rows)
}

// ClearReminderDue converts a timed reminder back to on-join delivery (due_at = NULL),
// used when its due time arrives but the owner is offline.
func (d *Database) ClearReminderDue(publicID string) error {
	_, err := d.db.Exec(`UPDATE reminders SET due_at = NULL WHERE public_id = ?`, publicID)
	return err
}

// ListJoinReminders returns the legacy on-join reminders (due_at IS NULL) for ownerNick.
func (d *Database) ListJoinReminders(ownerNick string) ([]Reminder, error) {
	targetFold := IRCCaseFoldNick(strings.TrimSpace(ownerNick))
	rows, err := d.db.Query(`
		SELECT public_id, owner_nick, note, created_at, due_at FROM reminders
		WHERE due_at IS NULL
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	all, err := scanReminders(rows)
	if err != nil {
		return nil, err
	}
	var out []Reminder
	for _, r := range all {
		if IRCCaseFoldNick(strings.TrimSpace(r.OwnerNick)) == targetFold {
			out = append(out, r)
		}
	}
	return out, nil
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

// GetReminder returns the reminder for publicID if it exists and ownerNick matches (IRC case fold).
func (d *Database) GetReminder(ownerNick, publicID string) (Reminder, bool, error) {
	publicID = strings.TrimSpace(publicID)
	var r Reminder
	if publicID == "" {
		return r, false, nil
	}
	var due sql.NullTime
	err := d.db.QueryRow(
		`SELECT public_id, owner_nick, note, created_at, due_at FROM reminders WHERE public_id = ?`,
		publicID,
	).Scan(&r.PublicID, &r.OwnerNick, &r.Note, &r.CreatedAt, &due)
	if due.Valid {
		r.DueAt = &due.Time
	}
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if IRCCaseFoldNick(strings.TrimSpace(r.OwnerNick)) != IRCCaseFoldNick(strings.TrimSpace(ownerNick)) {
		return r, false, nil
	}
	return r, true, nil
}

// ListReminders returns reminders for ownerNick, ordered by created_at ascending.
func (d *Database) ListReminders(ownerNick string) ([]Reminder, error) {
	uname := strings.TrimSpace(ownerNick)
	targetFold := IRCCaseFoldNick(uname)

	rows, err := d.db.Query(`
		SELECT public_id, owner_nick, note, created_at, due_at FROM reminders
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	all, err := scanReminders(rows)
	if err != nil {
		return nil, err
	}
	var out []Reminder
	for _, r := range all {
		if IRCCaseFoldNick(strings.TrimSpace(r.OwnerNick)) == targetFold {
			out = append(out, r)
		}
	}
	return out, nil
}

// scanReminders reads a rows set of (public_id, owner_nick, note, created_at, due_at).
func scanReminders(rows *sql.Rows) ([]Reminder, error) {
	defer rows.Close()
	var list []Reminder
	for rows.Next() {
		var r Reminder
		var due sql.NullTime
		if err := rows.Scan(&r.PublicID, &r.OwnerNick, &r.Note, &r.CreatedAt, &due); err != nil {
			return nil, err
		}
		if due.Valid {
			r.DueAt = &due.Time
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// AddTell stores a message for toNick, delivered when that nick is next seen.
func (d *Database) AddTell(fromNick, toNick, message string) (string, error) {
	fromNick = strings.TrimSpace(fromNick)
	toNick = strings.TrimSpace(toNick)
	message = strings.TrimSpace(message)
	if fromNick == "" || toNick == "" || message == "" {
		return "", fmt.Errorf("from, to and message required")
	}
	toFold := IRCCaseFoldNick(toNick)
	for range 8 {
		id, err := generateReminderPublicID()
		if err != nil {
			return "", err
		}
		_, err = d.db.Exec(
			`INSERT INTO tells (public_id, from_nick, to_fold, to_nick, message, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, fromNick, toFold, toNick, message, time.Now(),
		)
		if err == nil {
			return id, nil
		}
		if strings.Contains(err.Error(), "UNIQUE") {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("could not allocate unique tell id")
}

// PendingTellFolds returns the distinct case-folded nicks that have waiting tells.
func (d *Database) PendingTellFolds() ([]string, error) {
	rows, err := d.db.Query(`SELECT DISTINCT to_fold FROM tells`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folds []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		folds = append(folds, f)
	}
	return folds, rows.Err()
}

// CountTellsFor returns how many undelivered tells are waiting for nick.
func (d *Database) CountTellsFor(nick string) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM tells WHERE to_fold = ?`, IRCCaseFoldNick(strings.TrimSpace(nick))).Scan(&n)
	return n, err
}

// TakeTells returns and removes all pending tells for nick (delivery is one-shot).
func (d *Database) TakeTells(nick string) ([]Tell, error) {
	toFold := IRCCaseFoldNick(strings.TrimSpace(nick))
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT public_id, from_nick, to_nick, message, created_at FROM tells WHERE to_fold = ? ORDER BY created_at ASC`, toFold)
	if err != nil {
		return nil, err
	}
	var tells []Tell
	for rows.Next() {
		var t Tell
		if err := rows.Scan(&t.PublicID, &t.FromNick, &t.ToNick, &t.Message, &t.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		tells = append(tells, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tells) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(`DELETE FROM tells WHERE to_fold = ?`, toFold); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return tells, nil
}

// RecordSeen upserts the last-seen record for nick (action e.g. "message", "join", "part", "quit").
func (d *Database) RecordSeen(nick, channel, action, message string) error {
	nick = strings.TrimSpace(nick)
	if nick == "" {
		return nil
	}
	_, err := d.db.Exec(`
		INSERT INTO seen (nick_fold, nick, channel, action, message, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(nick_fold) DO UPDATE SET
			nick = excluded.nick, channel = excluded.channel,
			action = excluded.action, message = excluded.message, last_seen = excluded.last_seen`,
		IRCCaseFoldNick(nick), nick, channel, action, message, time.Now())
	return err
}

// GetSeen returns the last-seen record for nick, if any.
func (d *Database) GetSeen(nick string) (Seen, bool, error) {
	var s Seen
	var channel, action, message sql.NullString
	err := d.db.QueryRow(
		`SELECT nick, channel, action, message, last_seen FROM seen WHERE nick_fold = ?`,
		IRCCaseFoldNick(strings.TrimSpace(nick)),
	).Scan(&s.Nick, &channel, &action, &message, &s.LastSeen)
	if err == sql.ErrNoRows {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	s.Channel, s.Action, s.Message = channel.String, action.String, message.String
	return s, true, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}
