package uploads

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	TypePaste = "paste"
	TypeFile  = "file"
)

type Upload struct {
	ID               int          `json:"id"`
	TicketID         string       `json:"ticket_id"`
	Token            string       `json:"token"`
	Username         string       `json:"username"`
	Title            string       `json:"title"`
	Description      string       `json:"description"`
	ContentPath      string       `json:"content_path"`
	ExpiresInDays    int          `json:"expires_in_days"`
	Status           string       `json:"status"` // pending_form, pending_approval, approved, cancelled, expired
	Channel          string       `json:"channel"`
	CreatedAt        time.Time    `json:"created_at"`
	ApprovedAt       sql.NullTime `json:"approved_at"`
	UploadType       string       `json:"upload_type"`
	OriginalFilename string       `json:"original_filename"`
	ContentType      string       `json:"content_type"`
	SizeBytes        int64        `json:"size_bytes"`
}

func (u *Upload) IsFile() bool {
	return u.UploadType == TypeFile
}

type Database struct {
	db        *sql.DB
	pastesDir string
	filesDir  string
}

func NewDatabase(dbPath, pastesDir, filesDir string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// Reduce "database is locked" when IRC and web hit the same DB concurrently.
	if _, err := db.Exec("PRAGMA busy_timeout = 8000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite busy_timeout: %w", err)
	}

	query := `
	CREATE TABLE IF NOT EXISTS uploads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ticket_id TEXT UNIQUE,
		token TEXT UNIQUE,
		username TEXT,
		title TEXT,
		description TEXT,
		content_path TEXT,
		expires_in_days INTEGER,
		status TEXT,
		channel TEXT,
		created_at DATETIME,
		approved_at DATETIME
	);`
	if _, err = db.Exec(query); err != nil {
		return nil, err
	}

	if err := migrateUploadsSchema(db); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(pastesDir, 0755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		return nil, err
	}

	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")

	return &Database{db: db, pastesDir: pastesDir, filesDir: filesDir}, nil
}

func migrateUploadsSchema(db *sql.DB) error {
	cols, err := tableColumns(db, "uploads")
	if err != nil {
		return err
	}
	add := func(name, ddl string) error {
		if cols[name] {
			return nil
		}
		_, err := db.Exec("ALTER TABLE uploads ADD COLUMN " + name + " " + ddl)
		if err != nil {
			return fmt.Errorf("add column %s: %w", name, err)
		}
		cols[name] = true
		return nil
	}
	if err := add("upload_type", "TEXT DEFAULT 'paste'"); err != nil {
		return err
	}
	if err := add("original_filename", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := add("content_type", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := add("size_bytes", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (d *Database) CreateUploadSession(token, username, channel string) error {
	q := `INSERT INTO uploads (token, username, channel, status, created_at, upload_type) VALUES (?, ?, ?, 'pending_form', ?, ?)`
	_, err := d.db.Exec(q, token, username, channel, time.Now(), TypePaste)
	return err
}

func (d *Database) CreateFileUploadSession(token, username, channel string) error {
	q := `INSERT INTO uploads (token, username, channel, status, created_at, upload_type) VALUES (?, ?, ?, 'pending_form', ?, ?)`
	_, err := d.db.Exec(q, token, username, channel, time.Now(), TypeFile)
	return err
}

func (d *Database) GetUploadByToken(token string) (*Upload, error) {
	row := d.db.QueryRow(`
		SELECT id, token,
		       COALESCE(username,''), COALESCE(channel,''), COALESCE(status,''), created_at,
		       COALESCE(upload_type, 'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE token = ?`, token)
	var u Upload
	err := row.Scan(&u.ID, &u.Token, &u.Username, &u.Channel, &u.Status, &u.CreatedAt,
		&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *Database) SubmitUpload(token, ticketID, title, description, content string, expiresInDays int) error {
	fileName := fmt.Sprintf("%s.txt", ticketID)
	filePath := filepath.Join(d.pastesDir, fileName)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return err
	}

	q := `UPDATE uploads SET ticket_id = ?, title = ?, description = ?, content_path = ?, expires_in_days = ?, status = 'pending_approval', upload_type = ? WHERE token = ?`
	_, err := d.db.Exec(q, ticketID, title, description, filePath, expiresInDays, TypePaste, token)
	return err
}

func (d *Database) SubmitFileUpload(token, ticketID, title, description string, expiresInDays int, diskPath, originalFilename, contentType string, sizeBytes int64) error {
	q := `UPDATE uploads SET ticket_id = ?, title = ?, description = ?, content_path = ?, expires_in_days = ?, status = 'pending_approval',
		upload_type = ?, original_filename = ?, content_type = ?, size_bytes = ? WHERE token = ?`
	_, err := d.db.Exec(q, ticketID, title, description, diskPath, expiresInDays, TypeFile, originalFilename, contentType, sizeBytes, token)
	return err
}

func (d *Database) CancelUploadByToken(token string) (string, string, error) {
	var username, channel string
	err := d.db.QueryRow("SELECT username, channel FROM uploads WHERE token = ?", token).Scan(&username, &channel)
	if err != nil {
		return "", "", err
	}
	_, err = d.db.Exec(`UPDATE uploads SET status = 'cancelled' WHERE token = ?`, token)
	return username, channel, err
}

func (d *Database) ApproveTicket(ticketID string) error {
	q := `UPDATE uploads SET status = 'approved', approved_at = ? WHERE ticket_id = ?`
	_, err := d.db.Exec(q, time.Now(), ticketID)
	return err
}

func (d *Database) CancelTicket(ticketID string) error {
	var contentPath string
	_ = d.db.QueryRow("SELECT content_path FROM uploads WHERE ticket_id = ?", ticketID).Scan(&contentPath)
	if contentPath != "" {
		_ = os.Remove(contentPath)
	}
	q := `UPDATE uploads SET status = 'cancelled' WHERE ticket_id = ?`
	_, err := d.db.Exec(q, ticketID)
	return err
}

func (d *Database) GetUploadByTicketID(ticketID string) (*Upload, error) {
	row := d.db.QueryRow(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at,
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE ticket_id = ?`, ticketID)
	var u Upload
	err := row.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
		&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *Database) GetApprovedPastes(limit, offset int) ([]*Upload, int, error) {
	where := `status = 'approved' AND COALESCE(upload_type,'paste') = 'paste'`
	var total int
	err := d.db.QueryRow("SELECT COUNT(*) FROM uploads WHERE " + where).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at,
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE `+where+` ORDER BY approved_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return scanUploadRows(rows, total)
}

func (d *Database) GetApprovedFiles(limit, offset int) ([]*Upload, int, error) {
	where := `status = 'approved' AND upload_type = 'file'`
	var total int
	err := d.db.QueryRow("SELECT COUNT(*) FROM uploads WHERE " + where).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at,
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE `+where+` ORDER BY approved_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return scanUploadRows(rows, total)
}

// ListApprovedFilesByUser returns approved file uploads for username, newest first (same rows as the web file list).
// limit must be positive.
func (d *Database) ListApprovedFilesByUser(username string, limit int) ([]*Upload, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	uname := strings.TrimSpace(username)
	targetFold := ircFoldNick(uname)

	// Align with GetApprovedFiles (no expiry filter in SQL — dashboard lists all approved files).
	// IRC nicks: trim + case-insensitive; LOWER matches most cases.
	q := `
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at,
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE status = 'approved' AND upload_type = 'file'
			AND LOWER(TRIM(COALESCE(username,''))) = LOWER(?)
		ORDER BY approved_at DESC LIMIT ?`
	rows, err := d.db.Query(q, uname, limit)
	if err != nil {
		return nil, err
	}
	list, err := scanApprovedFileRows(rows)
	if err != nil {
		return nil, err
	}
	if len(list) > 0 {
		return list, nil
	}

	// Fallback: RFC 1459 casefold can differ from ASCII LOWER (e.g. [ vs {).
	rows2, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at,
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE status = 'approved' AND upload_type = 'file'
		ORDER BY approved_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	var out []*Upload
	for rows2.Next() {
		var u Upload
		err := rows2.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
		if err != nil {
			return nil, err
		}
		if ircFoldNick(strings.TrimSpace(u.Username)) == targetFold {
			out = append(out, &u)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, rows2.Err()
}

func scanApprovedFileRows(rows *sql.Rows) ([]*Upload, error) {
	defer rows.Close()
	var list []*Upload
	for rows.Next() {
		var u Upload
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
		if err != nil {
			return nil, err
		}
		list = append(list, &u)
	}
	return list, rows.Err()
}

// ircFoldNick applies RFC 1459 ASCII case mapping for IRC nick comparison ([→{, ]→}, etc.).
func ircFoldNick(s string) string {
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

func scanUploadRows(rows *sql.Rows, total int) ([]*Upload, int, error) {
	var list []*Upload
	for rows.Next() {
		var u Upload
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
		if err != nil {
			return nil, 0, err
		}
		list = append(list, &u)
	}
	return list, total, rows.Err()
}

// GetPendingTickets returns all items awaiting approval (pastes and files) — for IRC.
func (d *Database) GetPendingTickets() ([]*Upload, error) {
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, created_at,
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE status = 'pending_approval' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Upload
	for rows.Next() {
		var u Upload
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.CreatedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
		if err != nil {
			return nil, err
		}
		list = append(list, &u)
	}
	return list, rows.Err()
}

// GetPendingPastes returns only text pastes pending approval — for web Pastes panel.
func (d *Database) GetPendingPastes() ([]*Upload, error) {
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, created_at,
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE status = 'pending_approval' AND COALESCE(upload_type,'paste') = 'paste' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Upload
	for rows.Next() {
		var u Upload
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.CreatedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
		if err != nil {
			return nil, err
		}
		list = append(list, &u)
	}
	return list, rows.Err()
}

// GetPendingFiles returns only file uploads pending approval.
func (d *Database) GetPendingFiles() ([]*Upload, error) {
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, created_at,
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0)
		FROM uploads WHERE status = 'pending_approval' AND upload_type = 'file' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Upload
	for rows.Next() {
		var u Upload
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.CreatedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes)
		if err != nil {
			return nil, err
		}
		list = append(list, &u)
	}
	return list, rows.Err()
}

func (d *Database) DeletePaste(ticketID string) error {
	var contentPath string
	err := d.db.QueryRow("SELECT content_path FROM uploads WHERE ticket_id = ?", ticketID).Scan(&contentPath)
	if err == nil && contentPath != "" {
		_ = os.Remove(contentPath)
	}
	_, err = d.db.Exec(`DELETE FROM uploads WHERE ticket_id = ?`, ticketID)
	return err
}

// FilesDiskDir returns the directory used for binary uploads (for saving temp files).
func (d *Database) FilesDiskDir() string {
	return d.filesDir
}

// SafeFileExt returns a short safe extension from the original name.
func SafeFileExt(original string) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(original)))
	if ext == "" || len(ext) > 16 {
		return ".bin"
	}
	for _, r := range ext {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' {
			continue
		}
		return ".bin"
	}
	return ext
}

func (d *Database) Close() error {
	return d.db.Close()
}
