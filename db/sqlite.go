package db

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

// sqlitePragmaDSN is appended to every database path so modernc.org/sqlite applies
// these pragmas to EACH pooled connection (via its DSN query-param handling), not
// just whichever connection happens to run a one-off db.Exec("PRAGMA ..."). With a
// pool of up to 25 connections, only journal_mode (stored in the DB header) survives
// a db.Exec-based approach; busy_timeout/foreign_keys/etc. would silently be unset
// on every connection but the first.
const sqlitePragmaDSN = "?_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=cache_size(-64000)" +
	"&_pragma=temp_store(2)" // 2 = MEMORY

// OpenDatabase opens a SQLite database with performance optimizations.
func OpenDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath+sqlitePragmaDSN)
	if err != nil {
		return nil, err
	}

	// Set connection pool size
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
