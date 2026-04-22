package uploads

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
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
	DownloadCount    int          `json:"download_count"`
	PublicRef        string       `json:"public_ref"`
	PasteKind        string       `json:"paste_kind"` // Linguist-style label; "plain text" for non-code; empty for file uploads
	ClientHost       string       `json:"client_host"`
	MD5Hex           string       `json:"md5_hex"`
	SHA256Hex        string       `json:"sha256_hex"`
	IsPublic         bool         `json:"is_public"`
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

	d := &Database{db: db, pastesDir: pastesDir, filesDir: filesDir}
	if err := d.backfillLegacyUploadRows(); err != nil {
		db.Close()
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

	return d, nil
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
	if err := add("download_count", "INTEGER DEFAULT 0"); err != nil {
		return err
	}
	if err := add("public_ref", "TEXT"); err != nil {
		return err
	}
	if err := add("client_host", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := add("md5_hex", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := add("sha256_hex", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	if err := add("is_public", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := add("paste_kind", "TEXT DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func (d *Database) backfillLegacyUploadRows() error {
	for {
		var id int
		err := d.db.QueryRow(`SELECT id FROM uploads WHERE public_ref IS NULL OR TRIM(COALESCE(public_ref,'')) = '' LIMIT 1`).Scan(&id)
		if err == sql.ErrNoRows {
			break
		}
		if err != nil {
			return fmt.Errorf("backfill public_ref select: %w", err)
		}
		ref, err := NewPublicRef()
		if err != nil {
			return err
		}
		if _, err := d.db.Exec(`UPDATE uploads SET public_ref = ? WHERE id = ?`, ref, id); err != nil {
			return fmt.Errorf("backfill public_ref update: %w", err)
		}
	}
	rows, err := d.db.Query(`
		SELECT id, content_path FROM uploads
		WHERE COALESCE(upload_type,'paste') = 'paste'
		  AND TRIM(COALESCE(content_path,'')) != ''
		  AND (COALESCE(size_bytes,0) = 0 OR COALESCE(md5_hex,'') = '')`)
	if err != nil {
		return fmt.Errorf("backfill paste meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var cpath string
		if err := rows.Scan(&id, &cpath); err != nil {
			return err
		}
		st, err := os.Stat(cpath)
		if err != nil {
			continue
		}
		mdH, shH, err := HexMD5SHA256FromFile(cpath)
		if err != nil {
			continue
		}
		_, err = d.db.Exec(`UPDATE uploads SET size_bytes = ?, md5_hex = ?, sha256_hex = ? WHERE id = ?`,
			st.Size(), mdH, shH, id)
		if err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows2, err := d.db.Query(`
		SELECT id, content_path FROM uploads
		WHERE upload_type = 'file' AND TRIM(COALESCE(content_path,'')) != ''
		  AND (COALESCE(md5_hex,'') = '' OR COALESCE(sha256_hex,'') = '')`)
	if err != nil {
		return fmt.Errorf("backfill file hashes: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id int
		var cpath string
		if err := rows2.Scan(&id, &cpath); err != nil {
			return err
		}
		mdH, shH, err := HexMD5SHA256FromFile(cpath)
		if err != nil {
			continue
		}
		st, err := os.Stat(cpath)
		if err != nil {
			continue
		}
		_, err = d.db.Exec(`UPDATE uploads SET md5_hex = ?, sha256_hex = ?, size_bytes = ? WHERE id = ?`,
			mdH, shH, st.Size(), id)
		if err != nil {
			return err
		}
	}
	if err := rows2.Err(); err != nil {
		return err
	}

	rows3, err := d.db.Query(`
		SELECT id, content_path FROM uploads
		WHERE COALESCE(upload_type,'paste') = 'paste' AND TRIM(COALESCE(content_path,'')) != ''
		  AND TRIM(COALESCE(paste_kind,'')) = ''`)
	if err != nil {
		return fmt.Errorf("backfill paste_kind select: %w", err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var id int
		var cpath string
		if err := rows3.Scan(&id, &cpath); err != nil {
			return err
		}
		b, err := os.ReadFile(cpath)
		if err != nil {
			continue
		}
		kind := ClassifyPasteText(b)
		if _, err := d.db.Exec(`UPDATE uploads SET paste_kind = ? WHERE id = ?`, kind, id); err != nil {
			return err
		}
	}
	return rows3.Err()
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
	ref, err := NewPublicRef()
	if err != nil {
		return err
	}
	q := `INSERT INTO uploads (token, username, channel, status, created_at, upload_type, public_ref, is_public) VALUES (?, ?, ?, 'pending_form', ?, ?, ?, 1)`
	_, err = d.db.Exec(q, token, username, channel, time.Now(), TypePaste, ref)
	return err
}

func (d *Database) CreateFileUploadSession(token, username, channel string) error {
	ref, err := NewPublicRef()
	if err != nil {
		return err
	}
	q := `INSERT INTO uploads (token, username, channel, status, created_at, upload_type, public_ref, is_public) VALUES (?, ?, ?, 'pending_form', ?, ?, ?, 0)`
	_, err = d.db.Exec(q, token, username, channel, time.Now(), TypeFile, ref)
	return err
}

func (d *Database) GetUploadByToken(token string) (*Upload, error) {
	row := d.db.QueryRow(`
		SELECT id, COALESCE(ticket_id,''), token,
		       COALESCE(username,''), COALESCE(channel,''), COALESCE(status,''), created_at,
		       COALESCE(upload_type, 'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE token = ?`, token)
	var u Upload
	var ipub int
	err := row.Scan(&u.ID, &u.TicketID, &u.Token, &u.Username, &u.Channel, &u.Status, &u.CreatedAt,
		&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
		&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
	if err != nil {
		return nil, err
	}
	u.IsPublic = ipub != 0
	return &u, nil
}

func (d *Database) SubmitUpload(token, ticketID, title, description, content string, expiresInDays int, clientHost string) error {
	fileName := fmt.Sprintf("%s.txt", ticketID)
	filePath := filepath.Join(d.pastesDir, fileName)
	body := []byte(content)
	if err := os.WriteFile(filePath, body, 0644); err != nil {
		return err
	}
	mdH, shH := HexMD5SHA256FromBytes(body)
	size := int64(len(body))
	kind := ClassifyPasteText(body)

	q := `UPDATE uploads SET ticket_id = ?, title = ?, description = ?, content_path = ?, expires_in_days = ?, status = 'pending_approval', upload_type = ?,
		size_bytes = ?, client_host = ?, md5_hex = ?, sha256_hex = ?, paste_kind = ? WHERE token = ?`
	_, err := d.db.Exec(q, ticketID, title, description, filePath, expiresInDays, TypePaste, size, clientHost, mdH, shH, kind, token)
	return err
}

func (d *Database) SubmitFileUpload(token, ticketID, title, description string, expiresInDays int, diskPath, originalFilename, contentType string, sizeBytes int64, clientHost, md5Hex, sha256Hex string) error {
	q := `UPDATE uploads SET ticket_id = ?, title = ?, description = ?, content_path = ?, expires_in_days = ?, status = 'pending_approval',
		upload_type = ?, original_filename = ?, content_type = ?, size_bytes = ?, client_host = ?, md5_hex = ?, sha256_hex = ? WHERE token = ?`
	_, err := d.db.Exec(q, ticketID, title, description, diskPath, expiresInDays, TypeFile, originalFilename, contentType, sizeBytes, clientHost, md5Hex, sha256Hex, token)
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
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE ticket_id = ?`, ticketID)
	var u Upload
	var ipub int
	err := row.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
		&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
		&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
	if err != nil {
		return nil, err
	}
	u.IsPublic = ipub != 0
	return &u, nil
}

// GetApprovedPastes returns approved text pastes, newest first.
// If excludePublicExpired is true, rows past their public expiry (approved_at + expires_in_days) are omitted; use for non-staff list views.
// Expiry is applied in Go (IsAccessExpired) so it matches /p/ and survives SQLite/driver timestamp formats.
func (d *Database) GetApprovedPastes(limit, offset int, now time.Time, excludePublicExpired bool) ([]*Upload, int, error) {
	where := `status = 'approved' AND COALESCE(upload_type,'paste') = 'paste'`
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at,
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE `+where+` ORDER BY approved_at DESC`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, _, err := scanUploadRows(rows, 0)
	if err != nil {
		return nil, 0, err
	}
	var filtered []*Upload
	if excludePublicExpired {
		for _, u := range list {
			if !u.IsAccessExpired(now) {
				filtered = append(filtered, u)
			}
		}
	} else {
		filtered = list
	}
	total := len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []*Upload{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}

func (d *Database) GetApprovedFiles(limit, offset int, publicOnly bool) ([]*Upload, int, error) {
	where := `status = 'approved' AND upload_type = 'file'`
	if publicOnly {
		where += ` AND COALESCE(is_public, 1) != 0`
	}
	var total int
	err := d.db.QueryRow("SELECT COUNT(*) FROM uploads WHERE " + where).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at,
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE `+where+` ORDER BY approved_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return scanUploadRows(rows, total)
}

// SetFileIsPublic sets whether an approved file is anonymously downloadable / listed for non-staff users.
func (d *Database) SetFileIsPublic(ticketID string, public bool) error {
	v := 0
	if public {
		v = 1
	}
	res, err := d.db.Exec(`
		UPDATE uploads SET is_public = ?
		WHERE ticket_id = ? AND upload_type = 'file' AND status = 'approved'`, v, ticketID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
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
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
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
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE status = 'approved' AND upload_type = 'file'
		ORDER BY approved_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	var out []*Upload
	for rows2.Next() {
		var u Upload
		var ipub int
		err := rows2.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
			&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
		if err != nil {
			return nil, err
		}
		u.IsPublic = ipub != 0
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
		var ipub int
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
			&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
		if err != nil {
			return nil, err
		}
		u.IsPublic = ipub != 0
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
		var ipub int
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
			&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
		if err != nil {
			return nil, 0, err
		}
		u.IsPublic = ipub != 0
		list = append(list, &u)
	}
	return list, total, rows.Err()
}

// GetPendingTickets returns all items awaiting approval (pastes and files) — for IRC.
func (d *Database) GetPendingTickets() ([]*Upload, error) {
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, created_at,
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE status = 'pending_approval' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Upload
	for rows.Next() {
		var u Upload
		var ipub int
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.CreatedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
			&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
		if err != nil {
			return nil, err
		}
		u.IsPublic = ipub != 0
		list = append(list, &u)
	}
	return list, rows.Err()
}

// GetPendingPastes returns only text pastes pending approval — for web Pastes panel.
func (d *Database) GetPendingPastes() ([]*Upload, error) {
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, created_at,
		       COALESCE(upload_type,'paste'), COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE status = 'pending_approval' AND COALESCE(upload_type,'paste') = 'paste' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Upload
	for rows.Next() {
		var u Upload
		var ipub int
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.CreatedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
			&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
		if err != nil {
			return nil, err
		}
		u.IsPublic = ipub != 0
		list = append(list, &u)
	}
	return list, rows.Err()
}

// GetPendingFiles returns only file uploads pending approval.
func (d *Database) GetPendingFiles() ([]*Upload, error) {
	rows, err := d.db.Query(`
		SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, created_at,
		       upload_type, COALESCE(original_filename,''), COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(download_count, 0),
		       COALESCE(public_ref,''), COALESCE(paste_kind,''), COALESCE(client_host,''), COALESCE(md5_hex,''), COALESCE(sha256_hex,''),
		       COALESCE(is_public, 1)
		FROM uploads WHERE status = 'pending_approval' AND upload_type = 'file' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Upload
	for rows.Next() {
		var u Upload
		var ipub int
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.CreatedAt,
			&u.UploadType, &u.OriginalFilename, &u.ContentType, &u.SizeBytes, &u.DownloadCount,
			&u.PublicRef, &u.PasteKind, &u.ClientHost, &u.MD5Hex, &u.SHA256Hex, &ipub)
		if err != nil {
			return nil, err
		}
		u.IsPublic = ipub != 0
		list = append(list, &u)
	}
	return list, rows.Err()
}

// IncrementFileDownloadCount bumps the counter for an approved file ticket (no-op if row missing or not a file).
func (d *Database) IncrementFileDownloadCount(ticketID string) error {
	_, err := d.db.Exec(`
		UPDATE uploads SET download_count = COALESCE(download_count, 0) + 1
		WHERE ticket_id = ? AND upload_type = 'file' AND status = 'approved'`, ticketID)
	return err
}

// CountPendingPastes returns how many text pastes await approval.
func (d *Database) CountPendingPastes() (int, error) {
	var n int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM uploads
		WHERE status = 'pending_approval' AND COALESCE(upload_type,'paste') = 'paste'`).Scan(&n)
	return n, err
}

// CountPendingFiles returns how many file uploads await approval.
func (d *Database) CountPendingFiles() (int, error) {
	var n int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM uploads
		WHERE status = 'pending_approval' AND upload_type = 'file'`).Scan(&n)
	return n, err
}

// CountPendingApproval returns uploads awaiting approval (pastes and files).
func (d *Database) CountPendingApproval() (int, error) {
	var n int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM uploads WHERE status = 'pending_approval'`).Scan(&n)
	return n, err
}

// CountApprovedPastes returns approved text paste rows in the database.
func (d *Database) CountApprovedPastes() (int, error) {
	var n int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM uploads
		WHERE status = 'approved' AND COALESCE(upload_type,'paste') = 'paste'`).Scan(&n)
	return n, err
}

// CountApprovedFiles returns approved file upload rows in the database.
func (d *Database) CountApprovedFiles() (int, error) {
	var n int
	err := d.db.QueryRow(`
		SELECT COUNT(*) FROM uploads
		WHERE status = 'approved' AND upload_type = 'file'`).Scan(&n)
	return n, err
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

// ReplaceApprovedFileContent updates stored path and metadata after replacing the on-disk blob (e.g. compress to .tgz).
func (d *Database) ReplaceApprovedFileContent(ticketID, newPath, origName, contentType string, sizeBytes int64, md5Hex, sha256Hex string) error {
	res, err := d.db.Exec(`
		UPDATE uploads SET content_path = ?, original_filename = ?, content_type = ?, size_bytes = ?, md5_hex = ?, sha256_hex = ?
		WHERE ticket_id = ? AND upload_type = 'file' AND status = 'approved'`,
		newPath, origName, contentType, sizeBytes, md5Hex, sha256Hex, ticketID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
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

// HexMD5SHA256FromBytes returns lowercase hex digests of b.
func HexMD5SHA256FromBytes(b []byte) (md5Hex, sha256Hex string) {
	h1 := md5.Sum(b)
	h2 := sha256.Sum256(b)
	return hex.EncodeToString(h1[:]), hex.EncodeToString(h2[:])
}

// HexMD5SHA256FromFile reads path and returns lowercase hex digests.
func HexMD5SHA256FromFile(path string) (md5Hex, sha256Hex string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	mw := md5.New()
	sw := sha256.New()
	if _, err := io.Copy(io.MultiWriter(mw, sw), f); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(mw.Sum(nil)), hex.EncodeToString(sw.Sum(nil)), nil
}

// NewPublicRef returns a 16-character lowercase hex string (8 random bytes).
func NewPublicRef() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (d *Database) Close() error {
	return d.db.Close()
}
