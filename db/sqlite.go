package db

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

// sqlitePragmaDSN is appended to every database path so modernc.org/sqlite applies
// these pragmas to EACH pooled connection (via its DSN query-param handling), not
// just whichever connection happens to run a one-off db.Exec("PRAGMA ..."). With a
// pool of several connections, only journal_mode (stored in the DB header) survives
// a db.Exec-based approach; busy_timeout/foreign_keys/etc. would silently be unset
// on every connection but the first.
const sqlitePragmaDSN = "?_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=cache_size(-16000)" +
	"&_pragma=temp_store(2)" // 2 = MEMORY

const maxOpenConns = 8

// OpenDatabase opens a SQLite database with performance optimizations.
func OpenDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath+sqlitePragmaDSN)
	if err != nil {
		return nil, err
	}

	// WAL allows many readers but one writer, so a big pool only adds contention. The page cache is
	// per connection (16 MB here), so the pool size also bounds memory: 9 databases x 8 x 16 MB.
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
