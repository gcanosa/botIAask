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
	ID           string  `json:"id"`
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	CurrentPrice float64 `json:"current_price"`
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
			PriceUSD:  m.CurrentPrice,
			FetchedAt: now,
		})
	}

	return f.db.SavePrices(entries)
}
