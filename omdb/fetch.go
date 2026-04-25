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
	host := strings.ToLower(strings.TrimSpace(u.Host))
	if u.Scheme == "http" && (host == "www.omdbapi.com" || host == "omdbapi.com") {
		u.Scheme = "https"
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
	// OMDb sits behind Cloudflare; stale cached 401/invalid-key responses are a known pain point.
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var p apiPayload
	jsonErr := json.Unmarshal(body, &p)

	// OMDb often returns HTTP 401 with JSON {Response:"False", Error:"Invalid API key!"} — surface Error instead of "http 401".
	if jsonErr == nil && strings.EqualFold(strings.TrimSpace(p.Response), "False") {
		msg := strings.TrimSpace(p.Error)
		if msg == "" {
			if resp.StatusCode != http.StatusOK {
				msg = fmt.Sprintf("request failed (http %d)", resp.StatusCode)
			} else {
				msg = "not found"
			}
		}
		if resp.StatusCode == http.StatusUnauthorized && strings.Contains(strings.ToLower(msg), "invalid api key") {
			msg += " — activate the key from your OMDb email, or check omdb.api_key / OMDB_API_KEY"
		}
		return &Result{OK: false, Error: msg}, nil
	}

	if resp.StatusCode != http.StatusOK {
		if jsonErr == nil && strings.TrimSpace(p.Error) != "" {
			return nil, fmt.Errorf("omdb: %s (http %d)", strings.TrimSpace(p.Error), resp.StatusCode)
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 120 {
			snippet = snippet[:120] + "…"
		}
		if snippet != "" {
			return nil, fmt.Errorf("omdb: http %d: %s", resp.StatusCode, snippet)
		}
		return nil, fmt.Errorf("omdb: http %d", resp.StatusCode)
	}

	if jsonErr != nil {
		return nil, fmt.Errorf("omdb: json: %w", jsonErr)
	}

	return &Result{
		OK:         true,
		Title:      strings.TrimSpace(p.Title),
		Year:       strings.TrimSpace(p.Year),
		ImdbRating: strings.TrimSpace(p.ImdbRating),
		Plot:       strings.TrimSpace(p.Plot),
	}, nil
}
