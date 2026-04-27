package crypto

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type PriceEntry struct {
	Symbol    string    `json:"symbol"`
	Name      string    `json:"name"`
	GeckoID   string    `json:"gecko_id,omitempty"`
	PriceUSD  float64   `json:"price"`
	Change24h float64   `json:"change_24h"`
	FetchedAt time.Time `json:"fetched_at"`
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
			change_24h REAL DEFAULT 0,
			fetched_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	// Migration: Ensure change_24h column exists
	_, _ = db.Exec("ALTER TABLE crypto_prices ADD COLUMN change_24h REAL DEFAULT 0;")
	_, _ = db.Exec("ALTER TABLE crypto_prices ADD COLUMN gecko_id TEXT DEFAULT '';")

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS forex_rates (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			rate_key TEXT NOT NULL,
			value REAL NOT NULL,
			fetched_at DATETIME NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to migrate forex_rates: %w", err)
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_forex_rates_fetched_at ON forex_rates(fetched_at)`)

	return &Database{db: db}, nil
}

func (d *Database) SavePrices(entries []PriceEntry) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO crypto_prices (symbol, name, gecko_id, price_usd, change_24h, fetched_at) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range entries {
		_, err := stmt.Exec(e.Symbol, e.Name, e.GeckoID, e.PriceUSD, e.Change24h, e.FetchedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SaveForexSnapshot appends one row per key for historical exchange-rate tracking.
func (d *Database) SaveForexSnapshot(m map[string]float64, fetchedAt time.Time) error {
	if len(m) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO forex_rates (rate_key, value, fetched_at) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for k, v := range m {
		if _, err := stmt.Exec(k, v, fetchedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetLatestForexPerKey returns the most recent stored value for each rate_key.
func (d *Database) GetLatestForexPerKey() (map[string]float64, error) {
	rows, err := d.db.Query(`
		SELECT fr.rate_key, fr.value
		FROM forex_rates fr
		INNER JOIN (
			SELECT rate_key, MAX(fetched_at) AS mx
			FROM forex_rates
			GROUP BY rate_key
		) t ON fr.rate_key = t.rate_key AND fr.fetched_at = t.mx
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]float64)
	for rows.Next() {
		var key string
		var v float64
		if err := rows.Scan(&key, &v); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, rows.Err()
}

// GetForexPerKeySecondLatest returns, for each rate_key, the value from the previous
// snapshot (strictly before the latest fetched_at for that key). Use with current values
// to show change since the last stored fetch.
func (d *Database) GetForexPerKeySecondLatest() (map[string]float64, error) {
	rows, err := d.db.Query(`
		SELECT fr.rate_key, fr.value
		FROM forex_rates fr
		INNER JOIN (
			SELECT fr2.rate_key, MAX(fr2.fetched_at) AS prev_at
			FROM forex_rates fr2
			INNER JOIN (
				SELECT rate_key, MAX(fetched_at) AS latest_at
				FROM forex_rates
				GROUP BY rate_key
			) latest ON fr2.rate_key = latest.rate_key
			WHERE fr2.fetched_at < latest.latest_at
			GROUP BY fr2.rate_key
		) p ON fr.rate_key = p.rate_key AND fr.fetched_at = p.prev_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]float64)
	for rows.Next() {
		var key string
		var v float64
		if err := rows.Scan(&key, &v); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, rows.Err()
}

// GetForexPerKeyAtOrBefore returns, for each rate_key, the value from the most recent
// snapshot with fetched_at <= t. Used to compare current rates to a past baseline (e.g. 24h ago).
func (d *Database) GetForexPerKeyAtOrBefore(t time.Time) (map[string]float64, error) {
	rows, err := d.db.Query(`
		SELECT fr.rate_key, fr.value
		FROM forex_rates fr
		INNER JOIN (
			SELECT rate_key, MAX(fetched_at) AS mx
			FROM forex_rates
			WHERE fetched_at <= ?
			GROUP BY rate_key
		) x ON fr.rate_key = x.rate_key AND fr.fetched_at = x.mx
	`, t)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]float64)
	for rows.Next() {
		var key string
		var v float64
		if err := rows.Scan(&key, &v); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, rows.Err()
}

func (d *Database) GetLatestPrices() ([]PriceEntry, error) {
	// We want the latest price for each symbol that was fetched in the last "batch"
	// For simplicity, we just fetch the last 10 entries ordered by fetched_at desc
	rows, err := d.db.Query(`
		SELECT symbol, name, COALESCE(gecko_id, ''), price_usd, change_24h, fetched_at 
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
		if err := rows.Scan(&e.Symbol, &e.Name, &e.GeckoID, &e.PriceUSD, &e.Change24h, &e.FetchedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	
	// If we don't have exactly "same second" fetches, this might return empty or partial.
	// Fallback: get last 10 unique symbols
	if len(entries) == 0 {
		rows, err = d.db.Query(`
			SELECT symbol, name, COALESCE(gecko_id, ''), price_usd, change_24h, fetched_at
			FROM (
				SELECT symbol, name, gecko_id, price_usd, change_24h, fetched_at,
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
			if err := rows.Scan(&e.Symbol, &e.Name, &e.GeckoID, &e.PriceUSD, &e.Change24h, &e.FetchedAt); err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
	}
	
	return entries, nil
}

// ForexHistoryRow is one stored snapshot point for a currency pair.
type ForexHistoryRow struct {
	Key       string
	Value     float64
	FetchedAt time.Time
}

// GetForexHistorySince returns all forex snapshot rows at or after since, oldest first.
func (d *Database) GetForexHistorySince(since time.Time) ([]ForexHistoryRow, error) {
	rows, err := d.db.Query(`
		SELECT rate_key, value, fetched_at FROM forex_rates
		WHERE fetched_at >= ?
		ORDER BY fetched_at ASC
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ForexHistoryRow
	for rows.Next() {
		var r ForexHistoryRow
		if err := rows.Scan(&r.Key, &r.Value, &r.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *Database) Close() error {
	return d.db.Close()
}
