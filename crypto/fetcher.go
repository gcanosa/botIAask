package crypto

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type Fetcher struct {
	db       *Database
	interval time.Duration
}

type CoinGeckoMarket struct {
	ID                       string  `json:"id"`
	Symbol                   string  `json:"symbol"`
	Name                     string  `json:"name"`
	CurrentPrice             float64 `json:"current_price"`
	PriceChangePercentage24h float64 `json:"price_change_percentage_24h"`
}

func NewFetcher(db *Database) *Fetcher {
	return &Fetcher{
		db:       db,
		interval: 3 * time.Hour,
	}
}

func (f *Fetcher) Start() {
	log.Printf("Crypto Fetcher started. Interval: %v", f.interval)
	// Initial fetch
	if err := f.FetchAndSave(); err != nil {
		log.Printf("Initial crypto fetch failed: %v", err)
	}

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for range ticker.C {
		if err := f.FetchAndSave(); err != nil {
			log.Printf("Periodic crypto fetch failed: %v", err)
		}
	}
}

func (f *Fetcher) FetchAndSave() error {
	url := "https://api.coingecko.com/api/v3/coins/markets?vs_currency=usd&order=market_cap_desc&per_page=10&page=1&sparkline=false"

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CoinGecko API returned status: %s", resp.Status)
	}

	var markets []CoinGeckoMarket
	if err := json.NewDecoder(resp.Body).Decode(&markets); err != nil {
		return err
	}

	var entries []PriceEntry
	now := time.Now()
	for _, m := range markets {
		entries = append(entries, PriceEntry{
			Symbol:    strings.ToUpper(m.Symbol),
			Name:      m.Name,
			GeckoID:   m.ID,
			PriceUSD:  m.CurrentPrice,
			Change24h: m.PriceChangePercentage24h,
			FetchedAt: now,
		})
	}

	if err := f.db.SavePrices(entries); err != nil {
		return err
	}

	// Fetch and store 7-day historical data for each coin
	for _, m := range markets {
		if m.ID == "" {
			continue
		}
		// Fetch 7 days of market history, sleep between requests to avoid rate limiting
		pts, err := FetchMarketChartWithRetry(client, m.ID, "7")
		if err != nil {
			log.Printf("Failed to fetch market history for %s: %v", m.ID, err)
			continue
		}
		if len(pts) > 0 {
			if err := f.db.SaveMarketHistory(m.ID, strings.ToUpper(m.Symbol), pts); err != nil {
				log.Printf("Failed to save market history for %s: %v", m.ID, err)
			}
		}
		time.Sleep(2 * time.Second)
	}

	// Cleanup old data (keep last 90 days)
	cutoff := time.Now().AddDate(0, 0, -90)
	if err := f.db.CleanupMarketHistory(cutoff); err != nil {
		log.Printf("Failed to cleanup market history: %v", err)
	}

	return nil
}
