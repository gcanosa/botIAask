package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const userAgent = "botIAask-github-tracker (+https://github.com/)"

// eventsHTTPClient bounds each Events API request so one unresponsive/private repo can't
// stall the poll cycle (mirrors the dedicated-timeout-client house style in weather/, omdb/).
var eventsHTTPClient = &http.Client{Timeout: 15 * time.Second}

// apiBase is overridden in tests to point at an httptest.Server instead of the real API.
var apiBase = "https://api.github.com"

// RawEvent is one entry from GET /repos/{owner}/{repo}/events. Payload is decoded per
// event Type by events.go.
type RawEvent struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Actor struct {
		Login string `json:"login"`
	} `json:"actor"`
	Repo struct {
		Name string `json:"name"`
	} `json:"repo"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// FetchResult is the outcome of one Events API poll.
type FetchResult struct {
	Events        []RawEvent
	NotModified   bool // true on HTTP 304: ETag matched, no body was read
	ETag          string
	RateRemaining int // -1 if the header was absent
}

// FetchRepoEvents polls GitHub's repo Events API. When etag is non-empty it's sent as
// If-None-Match; a 304 response short-circuits with NotModified=true and no further
// parsing, costing nothing against the rate limit. token, when non-empty, is sent as a
// Bearer credential (works for both classic and fine-grained PATs).
func FetchRepoEvents(ctx context.Context, owner, repo, token, etag string) (*FetchResult, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/events", apiBase, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := eventsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	result := &FetchResult{
		ETag:          resp.Header.Get("ETag"),
		RateRemaining: parseRateRemaining(resp.Header.Get("X-RateLimit-Remaining")),
	}

	if resp.StatusCode == http.StatusNotModified {
		result.NotModified = true
		return result, nil
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return result, fmt.Errorf("github: rate limited (status %d, remaining %d)", resp.StatusCode, result.RateRemaining)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return result, fmt.Errorf("github: unexpected status %d: %s", resp.StatusCode, body)
	}

	if err := json.NewDecoder(resp.Body).Decode(&result.Events); err != nil {
		return result, fmt.Errorf("github: decode events: %w", err)
	}
	return result, nil
}

func parseRateRemaining(s string) int {
	if s == "" {
		return -1
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return -1
	}
	return n
}
