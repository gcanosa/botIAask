package web

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"botIAask/config"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type User struct {
	ID        int       `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type AuthDatabase struct {
	db *sql.DB
}

func NewAuthDatabase(dbPath string) (*AuthDatabase, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open auth database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS web_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT DEFAULT 'admin',
			needs_password_change INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS web_sessions (
			token TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL,
			FOREIGN KEY(user_id) REFERENCES web_users(id)
		);
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth tables: %w", err)
	}

	// Migration: Add needs_password_change if it doesn't exist
	_, _ = db.Exec("ALTER TABLE web_users ADD COLUMN needs_password_change INTEGER DEFAULT 0")

	return &AuthDatabase{db: db}, nil
}

func (a *AuthDatabase) CheckAndSeedInitialAdmin(cfg *config.Config) error {
	var count int
	err := a.db.QueryRow("SELECT COUNT(*) FROM web_users").Scan(&count)
	if err != nil {
		return err
	}

	if count == 0 {
		username := cfg.Web.Auth.Username
		password := cfg.Web.Auth.Password
		if username == "" {
			username = "admin"
		}
		if password == "" {
			password = "password"
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}

		_, err = a.db.Exec("INSERT INTO web_users (username, password_hash, needs_password_change) VALUES (?, ?, 1)", username, string(hash))
		if err != nil {
			return err
		}
		fmt.Printf("Initial admin account created: %s / %s (Change required on first login)\n", username, password)
	}
	return nil
}

func (a *AuthDatabase) Authenticate(username, password string) (int, bool, error) {
	var id int
	var hash string
	var needsChange bool
	err := a.db.QueryRow("SELECT id, password_hash, needs_password_change FROM web_users WHERE username = ?", username).Scan(&id, &hash, &needsChange)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, fmt.Errorf("invalid username or password")
		}
		return 0, false, err
	}

	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		return 0, false, fmt.Errorf("invalid username or password")
	}

	return id, needsChange, nil
}

func (a *AuthDatabase) CreateSession(userID int) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)

	expiresAt := time.Now().Add(24 * time.Hour)
	_, err := a.db.Exec("INSERT INTO web_sessions (token, user_id, expires_at) VALUES (?, ?, ?)", token, userID, expiresAt)
	if err != nil {
		return "", err
	}

	return token, nil
}

func (a *AuthDatabase) ValidateSession(token string) (int, bool, error) {
	var userID int
	var expiresAt time.Time
	var needsChange bool
	err := a.db.QueryRow(`
		SELECT s.user_id, s.expires_at, u.needs_password_change 
		FROM web_sessions s
		JOIN web_users u ON s.user_id = u.id
		WHERE s.token = ?`, token).Scan(&userID, &expiresAt, &needsChange)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, false, fmt.Errorf("invalid session")
		}
		return 0, false, err
	}

	if time.Now().After(expiresAt) {
		a.db.Exec("DELETE FROM web_sessions WHERE token = ?", token)
		return 0, false, fmt.Errorf("session expired")
	}

	return userID, needsChange, nil
}

func (a *AuthDatabase) DeleteSession(token string) error {
	_, err := a.db.Exec("DELETE FROM web_sessions WHERE token = ?", token)
	return err
}

// ActiveSessionUsernames returns distinct usernames with a valid, non-expired web admin session.
func (a *AuthDatabase) ActiveSessionUsernames() ([]string, error) {
	rows, err := a.db.Query(`
		SELECT DISTINCT u.username
		FROM web_sessions s
		JOIN web_users u ON s.user_id = u.id
		WHERE s.expires_at > ?`, time.Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		names = append(names, u)
	}
	return names, rows.Err()
}

func (a *AuthDatabase) GetUsers() ([]User, error) {
	rows, err := a.db.Query("SELECT id, username, role, created_at FROM web_users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

func (a *AuthDatabase) AddUser(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = a.db.Exec("INSERT INTO web_users (username, password_hash, needs_password_change) VALUES (?, ?, 0)", username, string(hash))
	return err
}

func (a *AuthDatabase) UpdatePassword(userID int, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = a.db.Exec("UPDATE web_users SET password_hash = ?, needs_password_change = 0 WHERE id = ?", string(hash), userID)
	return err
}

func (a *AuthDatabase) UpdateUserPassword(id string, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = a.db.Exec("UPDATE web_users SET password_hash = ? WHERE id = ?", string(hash), id)
	return err
}

func (a *AuthDatabase) RemoveUser(id string) error {
	_, err := a.db.Exec("DELETE FROM web_users WHERE id = ?", id)
	return err
}

func (a *AuthDatabase) Close() error {
	return a.db.Close()
}
