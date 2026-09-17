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
// Also used by the one-shot lookup calls below (FetchPullRequest/FetchIssue/FetchCommit),
// which are triggered on-demand by "!gh search" rather than the poll loop.
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

// newAPIRequest builds a GET request with the headers every GitHub REST call in this
// package needs (Accept, API version, User-Agent, and Bearer auth when token is set).
func newAPIRequest(ctx context.Context, url, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

// FetchRepoEvents polls GitHub's repo Events API. When etag is non-empty it's sent as
// If-None-Match; a 304 response short-circuits with NotModified=true and no further
// parsing, costing nothing against the rate limit. token, when non-empty, is sent as a
// Bearer credential (works for both classic and fine-grained PATs).
func FetchRepoEvents(ctx context.Context, owner, repo, token, etag string) (*FetchResult, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/events", apiBase, owner, repo)
	req, err := newAPIRequest(ctx, url, token)
	if err != nil {
		return nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
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

// PullRequestInfo is the subset of a single PR lookup used for a "!gh search" IRC reply.
type PullRequestInfo struct {
	Number  int
	Title   string
	State   string // "open" | "closed"
	Merged  bool
	Author  string
	HTMLURL string
}

// IssueInfo is the subset of a single issue lookup used for a "!gh search" IRC reply.
type IssueInfo struct {
	Number  int
	Title   string
	State   string // "open" | "closed"
	Author  string
	HTMLURL string
}

// CommitInfo is the subset of a single commit lookup used for a "!gh search" IRC reply.
type CommitInfo struct {
	SHA     string
	Message string // first line only
	Author  string
	HTMLURL string
}

// FetchPullRequest calls GET /repos/{owner}/{repo}/pulls/{number}. A 404 (no such PR,
// possibly because the number belongs to an issue instead) returns (nil, nil) rather than
// an error — that's a routine outcome for "!gh search"'s smart dispatch, not a failure.
func FetchPullRequest(ctx context.Context, owner, repo, token string, number int) (*PullRequestInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, repo, number)
	var body struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		Merged  bool   `json:"merged"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := fetchOne(ctx, url, token, &body); err != nil {
		return nil, err
	}
	if body.Number == 0 {
		return nil, nil
	}
	return &PullRequestInfo{
		Number: body.Number, Title: body.Title, State: body.State,
		Merged: body.Merged, Author: body.User.Login, HTMLURL: body.HTMLURL,
	}, nil
}

// FetchIssue calls GET /repos/{owner}/{repo}/issues/{number}. Returns (nil, nil) on 404.
func FetchIssue(ctx context.Context, owner, repo, token string, number int) (*IssueInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d", apiBase, owner, repo, number)
	var body struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := fetchOne(ctx, url, token, &body); err != nil {
		return nil, err
	}
	if body.Number == 0 {
		return nil, nil
	}
	return &IssueInfo{
		Number: body.Number, Title: body.Title, State: body.State,
		Author: body.User.Login, HTMLURL: body.HTMLURL,
	}, nil
}

// FetchCommit calls GET /repos/{owner}/{repo}/commits/{sha}. Returns (nil, nil) on 404.
func FetchCommit(ctx context.Context, owner, repo, token, sha string) (*CommitInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s", apiBase, owner, repo, sha)
	var body struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := fetchOne(ctx, url, token, &body); err != nil {
		return nil, err
	}
	if body.SHA == "" {
		return nil, nil
	}
	author := body.Author.Login
	if author == "" {
		author = body.Commit.Author.Name
	}
	return &CommitInfo{
		SHA: body.SHA, Message: firstLine(body.Commit.Message),
		Author: author, HTMLURL: body.HTMLURL,
	}, nil
}

// fetchOne performs a single authenticated GET and decodes a 200 response into out. A 404
// leaves out untouched and returns nil (callers detect "not found" via out's zero value) —
// these lookups are one-shot, on-demand calls (not part of the polling cycle), so unlike
// FetchRepoEvents there's no ETag/rate-limit bookkeeping to thread through.
func fetchOne(ctx context.Context, url, token string, out interface{}) error {
	req, err := newAPIRequest(ctx, url, token)
	if err != nil {
		return err
	}
	resp, err := eventsHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github: unexpected status %d: %s", resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
