package web

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"botIAask/crypto"
	"botIAask/irc"
)

type cryptoChartCacheEntry struct {
	at   time.Time
	body []byte
}

// forexSnapshot returns a copy of the cached forex rates and their update time, refreshing them
// when stale (1 hour). forexMu is held across the refresh, so concurrent dashboard loads share one
// upstream fetch instead of each firing their own (and racing on the cache fields).
func (s *Server) forexSnapshot() (map[string]float64, time.Time) {
	s.forexMu.Lock()
	defer s.forexMu.Unlock()

	// If cache is empty, retry — but only once per minute, so a persistently failing/
	// rate-limited upstream doesn't turn every dashboard page load into a fresh API hit.
	cacheEmpty := s.forexCache == nil || len(s.forexCache) == 0
	if (cacheEmpty && time.Since(s.forexLastAttempt) > 1*time.Minute) || time.Since(s.forexUpdate) > 1*time.Hour {
		s.forexLastAttempt = time.Now()
		forex := map[string]float64{}

		// EUR to USD
		if eurRates, err := irc.CachedFetchRates("EUR"); err == nil {
			if rate, ok := eurRates.Rates["USD"]; ok {
				forex["eur_usd"] = rate
			}
		}

		// USD to ARS (Official)
		if usdRates, err := irc.CachedFetchRates("USD"); err == nil {
			if rate, ok := usdRates.Rates["ARS"]; ok {
				forex["usd_ars"] = rate
			}
			if eurRate, ok := usdRates.Rates["EUR"]; ok && eurRate != 0 {
				forex["eur_ars"] = usdRates.Rates["ARS"] / eurRate
			}
		}

		// ARS Blue (Parallel) - Using Bluelytics API
		client := &http.Client{Timeout: 5 * time.Second}
		if resp, err := client.Get("https://api.bluelytics.com.ar/v2/latest"); err == nil {
			defer resp.Body.Close()
			var blueData struct {
				Blue struct {
					ValueAvg float64 `json:"value_avg"`
				} `json:"blue"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&blueData); err == nil {
				forex["usd_ars_blue"] = blueData.Blue.ValueAvg
			}
		}

		if len(forex) > 0 {
			s.forexCache = forex
			s.forexUpdate = time.Now()
			if s.cryptoDB != nil {
				if err := s.cryptoDB.SaveForexSnapshot(forex, s.forexUpdate); err != nil {
					log.Printf("forex snapshot save: %v", err)
				}
			}
		}
	}

	out := make(map[string]float64, len(s.forexCache))
	for k, v := range s.forexCache {
		out[k] = v
	}
	return out, s.forexUpdate
}

func (s *Server) handleFinance(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{}

	// Get Crypto Prices
	data["crypto"] = []crypto.PriceEntry{}
	var cryptoLastUpdate time.Time
	if s.cryptoDB != nil {
		prices, err := s.cryptoDB.GetLatestPrices()
		if err == nil && len(prices) > 0 {
			data["crypto"] = prices
			cryptoLastUpdate = prices[0].FetchedAt
		}
	}
	data["crypto_last_update"] = cryptoLastUpdate.Format(time.RFC3339)

	forexOut, forexUpdate := s.forexSnapshot()
	// Fill gaps (e.g. official USD/ARS) from DB when the live API omits a key but history exists.
	if s.cryptoDB != nil {
		if dbFX, err := s.cryptoDB.GetLatestForexPerKey(); err == nil {
			for k, v := range dbFX {
				if _, ok := forexOut[k]; !ok {
					forexOut[k] = v
				}
			}
		}
	}
	data["forex"] = forexOut
	data["forex_last_update"] = forexUpdate.Format(time.RFC3339)

	// % change vs previous stored snapshot (second-latest row per key), not 24h — matches "last fetch" up/down.
	forexChange := map[string]float64{}
	if s.cryptoDB != nil && len(forexOut) > 0 {
		baseline, err := s.cryptoDB.GetForexPerKeySecondLatest()
		if err == nil {
			for k, current := range forexOut {
				old, ok := baseline[k]
				if !ok || old == 0 {
					continue
				}
				forexChange[k] = (current - old) / old * 100
			}
		}
	}
	data["forex_change_24h"] = forexChange

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) handleCryptoChart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cryptoDB == nil {
		http.Error(w, "crypto unavailable", http.StatusServiceUnavailable)
		return
	}

	rangeKey := crypto.NormalizeRangeKey(r.URL.Query().Get("range"))
	if rangeKey == "" {
		rangeKey = "1w"
	}
	if _, err := crypto.RangeToWindow(rangeKey); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	const chartTTL = 10 * time.Minute
	s.cryptoChartMu.Lock()
	if s.cryptoChartCache != nil {
		if e, ok := s.cryptoChartCache[rangeKey]; ok && time.Since(e.at) < chartTTL {
			s.cryptoChartMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write(e.body)
			return
		}
	}
	s.cryptoChartMu.Unlock()

	prices, err := s.cryptoDB.GetLatestPrices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	win, err := crypto.RangeToWindow(rangeKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	raw := make([]crypto.MarketRawSeries, 0, len(prices))

	// Get historical data from local DB instead of live API (much faster)
	since := time.Now().Add(-win)
	for _, p := range prices {
		if p.GeckoID == "" {
			continue
		}
		pts, err := s.cryptoDB.GetMarketHistoryForCoin(p.GeckoID, since)
		if err != nil {
			log.Printf("GetMarketHistoryForCoin %s: %v", p.GeckoID, err)
			continue
		}
		if len(pts) < 2 {
			continue
		}
		raw = append(raw, crypto.MarketRawSeries{
			Symbol:  p.Symbol,
			GeckoID: p.GeckoID,
			Points:  pts,
		})
	}

	resp, err := crypto.BuildChartResponse(rangeKey, raw, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.cryptoChartMu.Lock()
	if s.cryptoChartCache == nil {
		s.cryptoChartCache = make(map[string]cryptoChartCacheEntry)
	}
	s.cryptoChartCache[rangeKey] = cryptoChartCacheEntry{at: time.Now(), body: body}
	s.evictExpiredCacheEntries(s.cryptoChartCache, 1*time.Hour)
	s.cryptoChartMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (s *Server) handleForexChart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cryptoDB == nil {
		http.Error(w, "crypto unavailable", http.StatusServiceUnavailable)
		return
	}

	rangeKey := crypto.NormalizeRangeKey(r.URL.Query().Get("range"))
	if rangeKey == "" {
		rangeKey = "1w"
	}
	win, err := crypto.RangeToWindow(rangeKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	const chartTTL = 5 * time.Minute
	s.forexChartMu.Lock()
	if s.forexChartCache != nil {
		if e, ok := s.forexChartCache[rangeKey]; ok && time.Since(e.at) < chartTTL {
			s.forexChartMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write(e.body)
			return
		}
	}
	s.forexChartMu.Unlock()

	since := time.Now().Add(-win)
	rows, err := s.cryptoDB.GetForexHistorySince(since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := crypto.BuildForexChartResponse(rangeKey, rows, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.forexChartMu.Lock()
	if s.forexChartCache == nil {
		s.forexChartCache = make(map[string]cryptoChartCacheEntry)
	}
	s.forexChartCache[rangeKey] = cryptoChartCacheEntry{at: time.Now(), body: body}
	s.evictExpiredCacheEntries(s.forexChartCache, 1*time.Hour)
	s.forexChartMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// evictExpiredCacheEntries removes old entries from a cache map to prevent unbounded memory growth.
func (s *Server) evictExpiredCacheEntries(cache map[string]cryptoChartCacheEntry, maxAge time.Duration) {
	now := time.Now()
	for k, e := range cache {
		if now.Sub(e.at) > maxAge {
			delete(cache, k)
		}
	}
}
