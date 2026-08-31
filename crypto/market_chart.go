package crypto

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &rateLimitError{status: resp.Status, retryAfter: retryAfterDuration(resp.Header.Get("Retry-After"))}
	}
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

// rateLimitError carries the server-requested backoff for a 429 response.
type rateLimitError struct {
	status     string
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("429 Too Many Requests: %s", e.status)
}

// retryAfterDuration parses a Retry-After header (seconds), defaulting to 10s if absent/invalid.
func retryAfterDuration(header string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 10 * time.Second
}

// FetchMarketChartWithRetry fetches market_chart with small backoff on rate limits and empty payloads.
func FetchMarketChartWithRetry(client *http.Client, geckoID, days string) ([][2]float64, error) {
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			d := time.Duration(attempt*750) * time.Millisecond
			var rle *rateLimitError
			if errors.As(lastErr, &rle) {
				d = rle.retryAfter
			}
			time.Sleep(d)
		}
		pts, err := FetchMarketChart(client, geckoID, days)
		if err != nil {
			lastErr = err
			continue
		}
		if len(pts) >= 2 {
			return pts, nil
		}
		lastErr = fmt.Errorf("coingecko market_chart %s: insufficient points", geckoID)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("coingecko market_chart %s: no data", geckoID)
	}
	return nil, lastErr
}
