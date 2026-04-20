package crypto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// FetchMarketChart returns [timestamp_ms, price] pairs from CoinGecko (sorted ascending by time).
func FetchMarketChart(client *http.Client, geckoID, days string) ([][2]float64, error) {
	if geckoID == "" {
		return nil, fmt.Errorf("empty gecko id")
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	u := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/coins/%s/market_chart?vs_currency=usd&days=%s",
		url.PathEscape(geckoID),
		url.QueryEscape(days),
	)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coingecko market_chart %s: %s", geckoID, resp.Status)
	}

	var body struct {
		Prices [][2]float64 `json:"prices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Prices, nil
}
