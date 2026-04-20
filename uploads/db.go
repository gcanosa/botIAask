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
	ID            int
	TicketID      string
	Token         string
	Username      string
	Title         string
	Description   string
	ContentPath   string
	ExpiresInDays int
	Status        string // pending_form, pending_approval, approved, cancelled, expired
	Channel       string
	CreatedAt     time.Time
	ApprovedAt    sql.NullTime
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

func (d *Database) Close() error {
	return d.db.Close()
}
