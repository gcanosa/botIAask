package uploads

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Upload struct {
	ID            int          `json:"id"`
	TicketID      string       `json:"ticket_id"`
	Token         string       `json:"token"`
	Username      string       `json:"username"`
	Title         string       `json:"title"`
	Description   string       `json:"description"`
	ContentPath   string       `json:"content_path"`
	ExpiresInDays int          `json:"expires_in_days"`
	Status        string       `json:"status"` // pending_form, pending_approval, approved, cancelled, expired
	Channel       string       `json:"channel"`
	CreatedAt     time.Time    `json:"created_at"`
	ApprovedAt    sql.NullTime `json:"approved_at"`
}

type Database struct {
	db        *sql.DB
	pastesDir string
}

func NewDatabase(dbPath, pastesDir string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Create table if not exists
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
	_, err = db.Exec(query)
	if err != nil {
		return nil, err
	}

	// Ensure pastes directory exists
	if err := os.MkdirAll(pastesDir, 0755); err != nil {
		return nil, err
	}

	// Optimize SQLite for better concurrency and consistency
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")

	return &Database{db: db, pastesDir: pastesDir}, nil
}

func (d *Database) CreateUploadSession(token, username, channel string) error {
	query := `INSERT INTO uploads (token, username, channel, status, created_at) VALUES (?, ?, ?, 'pending_form', ?)`
	_, err := d.db.Exec(query, token, username, channel, time.Now())
	return err
}

func (d *Database) GetUploadByToken(token string) (*Upload, error) {
	row := d.db.QueryRow("SELECT id, token, username, channel, status, created_at FROM uploads WHERE token = ?", token)
	var u Upload
	err := row.Scan(&u.ID, &u.Token, &u.Username, &u.Channel, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *Database) SubmitUpload(token, ticketID, title, description, content string, expiresInDays int) error {
	// Create file for content
	fileName := fmt.Sprintf("%s.txt", ticketID)
	filePath := filepath.Join(d.pastesDir, fileName)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return err
	}

	query := `UPDATE uploads SET ticket_id = ?, title = ?, description = ?, content_path = ?, expires_in_days = ?, status = 'pending_approval' WHERE token = ?`
	_, err := d.db.Exec(query, ticketID, title, description, filePath, expiresInDays, token)
	return err
}

func (d *Database) CancelUploadByToken(token string) (string, string, error) {
    var username, channel string
    err := d.db.QueryRow("SELECT username, channel FROM uploads WHERE token = ?", token).Scan(&username, &channel)
    if err != nil {
        return "", "", err
    }
	query := `UPDATE uploads SET status = 'cancelled' WHERE token = ?`
	_, err = d.db.Exec(query, token)
	return username, channel, err
}

func (d *Database) ApproveTicket(ticketID string) error {
	query := `UPDATE uploads SET status = 'approved', approved_at = ? WHERE ticket_id = ?`
	_, err := d.db.Exec(query, time.Now(), ticketID)
	return err
}

func (d *Database) CancelTicket(ticketID string) error {
	query := `UPDATE uploads SET status = 'cancelled' WHERE ticket_id = ?`
	_, err := d.db.Exec(query, ticketID)
	return err
}

func (d *Database) GetUploadByTicketID(ticketID string) (*Upload, error) {
	row := d.db.QueryRow("SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at FROM uploads WHERE ticket_id = ?", ticketID)
	var u Upload
	err := row.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *Database) GetApprovedPastes(limit, offset int) ([]*Upload, int, error) {
	// Get count
	var total int
	err := d.db.QueryRow("SELECT COUNT(*) FROM uploads WHERE status = 'approved'").Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Get rows
	rows, err := d.db.Query("SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, approved_at FROM uploads WHERE status = 'approved' ORDER BY approved_at DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var uploads []*Upload
	for rows.Next() {
		var u Upload
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.ApprovedAt)
		if err != nil {
			return nil, 0, err
		}
		uploads = append(uploads, &u)
	}
	return uploads, total, nil
}

func (d *Database) GetPendingTickets() ([]*Upload, error) {
	rows, err := d.db.Query("SELECT id, ticket_id, username, title, description, content_path, expires_in_days, status, channel, created_at FROM uploads WHERE status = 'pending_approval' ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uploads []*Upload
	for rows.Next() {
		var u Upload
		err := rows.Scan(&u.ID, &u.TicketID, &u.Username, &u.Title, &u.Description, &u.ContentPath, &u.ExpiresInDays, &u.Status, &u.Channel, &u.CreatedAt)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, &u)
	}
	return uploads, nil
}

func (d *Database) DeletePaste(ticketID string) error {
	// Get file path first to delete the file
	var contentPath string
	err := d.db.QueryRow("SELECT content_path FROM uploads WHERE ticket_id = ?", ticketID).Scan(&contentPath)
	if err == nil && contentPath != "" {
		os.Remove(contentPath)
	}

	query := `DELETE FROM uploads WHERE ticket_id = ?`
	_, err = d.db.Exec(query, ticketID)
	return err
}

func (d *Database) Close() error {
	return d.db.Close()
}
