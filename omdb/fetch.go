package omdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const userAgent = "botIAask/omdb (+https://github.com/)"

// Result is a normalized OMDb title lookup response.
type Result struct {
	OK         bool
	Error      string
	Title      string
	Year       string
	ImdbRating string
	Plot       string
}

type apiPayload struct {
	Response   string `json:"Response"`
	Error      string `json:"Error"`
	Title      string `json:"Title"`
	Year       string `json:"Year"`
	ImdbRating string `json:"imdbRating"`
	Plot       string `json:"Plot"`
}

// FetchByTitle requests movie metadata by title (plot=short).
func FetchByTitle(ctx context.Context, client *http.Client, apiKey, baseURL, title string) (*Result, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("omdb: api key is empty")
	}
	t := strings.TrimSpace(title)
	if t == "" {
		return &Result{OK: false, Error: "title is empty"}, nil
	}
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = "https://www.omdbapi.com/"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("omdb: base url: %w", err)
	}
	q := u.Query()
	q.Set("apikey", key)
	q.Set("t", t)
	q.Set("plot", "short")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("omdb: http %d", resp.StatusCode)
	}

	var p apiPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("omdb: json: %w", err)
	}
	if strings.EqualFold(p.Response, "False") {
		msg := strings.TrimSpace(p.Error)
		if msg == "" {
			msg = "not found"
		}
		return &Result{OK: false, Error: msg}, nil
	}

	return &Result{
		OK:         true,
		Title:      strings.TrimSpace(p.Title),
		Year:       strings.TrimSpace(p.Year),
		ImdbRating: strings.TrimSpace(p.ImdbRating),
		Plot:       strings.TrimSpace(p.Plot),
	}, nil
}
