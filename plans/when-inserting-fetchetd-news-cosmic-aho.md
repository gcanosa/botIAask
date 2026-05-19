# Fix: is.gd URL Shortener Returns Error String as Short Link

## Context

When RSS news items are fetched and inserted into the database, the `short_link` field sometimes contains the string `"Error, database insert failed"` instead of a real short URL. This error string then gets broadcast to IRC as if it were a clickable link.

The root cause: the `is.gd` API returns HTTP 200 even when it fails internally, putting an error message like `"Error, database insert failed"` in the response body. The `ShortenURL()` function reads the body but never validates whether it looks like an actual `is.gd` URL before returning it. The error string flows directly into `entry.ShortLink`, gets stored in SQLite, and gets sent to IRC.

## Files to Modify

- `/Users/ethernet/coding/golang/botIAask/rss/fetcher.go` — `ShortenURL()` function at line 314

## The Fix

After `io.ReadAll`, trim whitespace and validate the response body starts with `https://is.gd/` or `http://is.gd/`. If it does not, log the bad response and fall back to the original long URL.

```go
func ShortenURL(longURL string) string {
    if longURL == "" {
        return ""
    }

    apiURL := fmt.Sprintf("https://is.gd/create.php?format=simple&url=%s", url.QueryEscape(longURL))

    resp, err := http.Get(apiURL)
    if err != nil {
        log.Printf("[RSS] Error shortening URL: %v", err)
        return longURL
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        log.Printf("[RSS] Shortener returned status: %s", resp.Status)
        return longURL
    }

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        log.Printf("[RSS] Error reading shortener response: %v", err)
        return longURL
    }

    s := strings.TrimSpace(string(body))
    if !strings.HasPrefix(s, "https://is.gd/") && !strings.HasPrefix(s, "http://is.gd/") {
        log.Printf("[RSS] Shortener returned unexpected response: %s", s)
        return longURL
    }

    return s
}
```

The only required import addition is `"strings"` (check if already imported in the file).

## Verification

1. Run the bot and trigger an RSS fetch — confirm no `"Error, database insert failed"` appears in IRC output.
2. Query the DB: `SELECT short_link FROM seen_news WHERE short_link NOT LIKE 'http%'` — should return 0 rows for new entries.
3. When is.gd returns an error, the log should show `[RSS] Shortener returned unexpected response: Error, database insert failed` and the original URL is used instead.
