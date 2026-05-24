package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// OpenDatabase opens a SQLite database with performance optimizations.
func OpenDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Set connection pool size
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)

	// Apply SQLite pragmas for better performance
	pragmas := []string{
		"PRAGMA synchronous = NORMAL",           // Faster writes, still safe
		"PRAGMA cache_size = -64000",            // 64MB cache
		"PRAGMA temp_store = MEMORY",            // Use memory for temp tables
		"PRAGMA busy_timeout = 5000",            // 5s lock timeout
		"PRAGMA journal_mode = WAL",             // Write-Ahead Logging for concurrency
		"PRAGMA query_only = FALSE",             // Allow writes
		"PRAGMA foreign_keys = ON",              // Enforce foreign key constraints
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %q failed: %w", pragma, err)
		}
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
