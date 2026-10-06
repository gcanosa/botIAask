package crypto

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSaveMarketHistoryIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	d, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	pts := [][2]float64{{1000, 1.0}, {2000, 2.0}}
	for i := 0; i < 3; i++ {
		if err := d.SaveMarketHistory("bitcoin", "BTC", pts); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.GetMarketHistoryForCoin("bitcoin", time.UnixMilli(0))
	if err != nil || len(got) != 2 {
		t.Fatalf("want 2 points after repeated saves, got %d (err %v)", len(got), err)
	}
}

func TestMigrationDedupesExistingHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	d, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-fix database: drop the unique index and insert duplicates.
	d.db.Exec(`DROP INDEX idx_market_history_point`)
	for i := 0; i < 3; i++ {
		d.db.Exec(`INSERT INTO crypto_market_history (gecko_id, symbol, price_usd, timestamp_ms, fetched_at) VALUES ('b','B',?,1000,?)`, float64(i), time.Now())
	}
	d.db.Close()
	d2, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := d2.GetMarketHistoryForCoin("b", time.UnixMilli(0))
	if len(got) != 1 || got[0][1] != 2 {
		t.Fatalf("want single newest row (price 2), got %v", got)
	}
}
