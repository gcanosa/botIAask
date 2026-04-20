package crypto

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type PriceEntry struct {
	Symbol   string
	Name     string
	PriceUSD float64
	FetchedAt time.Time
}

type Database struct {
	db *sql.DB
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open crypto database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS crypto_prices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT,
			name TEXT,
			price_usd REAL,
			fetched_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to create crypto_prices table: %w", err)
	}

	return &Database{db: db}, nil
}

func (d *Database) SavePrices(entries []PriceEntry) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO crypto_prices (symbol, name, price_usd, fetched_at) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		_, err := stmt.Exec(e.Symbol, e.Name, e.PriceUSD, e.FetchedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (d *Database) GetLatestPrices() ([]PriceEntry, error) {
	// We want the latest price for each symbol that was fetched in the last "batch"
	// For simplicity, we just fetch the last 10 entries ordered by fetched_at desc
	rows, err := d.db.Query(`
		SELECT symbol, name, price_usd, fetched_at 
		FROM crypto_prices 
		WHERE fetched_at = (SELECT MAX(fetched_at) FROM crypto_prices)
		ORDER BY price_usd DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []PriceEntry
	for rows.Next() {
		var e PriceEntry
		if err := rows.Scan(&e.Symbol, &e.Name, &e.PriceUSD, &e.FetchedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	
	// If we don't have exactly "same second" fetches, this might return empty or partial.
	// Fallback: get last 10 unique symbols
	if len(entries) == 0 {
		rows, err = d.db.Query(`
			SELECT symbol, name, price_usd, fetched_at
			FROM (
				SELECT symbol, name, price_usd, fetched_at,
				ROW_NUMBER() OVER(PARTITION BY symbol ORDER BY fetched_at DESC) as rn
				FROM crypto_prices
			)
			WHERE rn = 1
			ORDER BY price_usd DESC
			LIMIT 10
		`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var e PriceEntry
			if err := rows.Scan(&e.Symbol, &e.Name, &e.PriceUSD, &e.FetchedAt); err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
	}
	
	return entries, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}
