# RSS fetcher fixes, security/perf sweep, admin login redesign

## Context
User asked for: (1) an audit of the RSS fetcher/announcer — efficiency, missed or repeated news, spam behaviour;
(2) a general security/performance sweep; (3) a redesign of the ugly admin login dialog in the web UI.
The RSS review found real bugs that cause **repeated** announcements and **floods**; those get fixed first.

## Part 1 — RSS (rss/fetcher.go, rss/db.go, web/server.go)

### Confirmed problems
| # | Problem | Effect |
|---|---------|--------|
| R1 | `CleanupPerSource(retention)` (default 50) deletes rows still present in the live feed when a feed has more items than `retention_count` | Deleted items look new next cycle → **re-announced every cycle** |
| R2 | `handleRSSSettings` (web/server.go:2196) calls global `Cleanup(n)` → keeps n rows **total** across all sources | Saving RSS settings wipes other feeds' history → **mass re-announce** |
| R3 | `go s.rssFetcher.Fetch()` (web "Fetch now", server.go:1207) + ticker loop + ApplyConfig restart can run `Fetch` concurrently | Same item can be announced twice (different GUID, same dedup_key isn't UNIQUE) |
| R4 | New feed / empty DB → every item (often 30–100) broadcast at 3 s each | Channel flood for minutes |
| R5 | Items marked seen *before* broadcast, even if IRC dropped mid-loop | News **missed** on disconnect |
| R6 | `NewsItemDuplicate` uses `TRIM(COALESCE(col,''))` in WHERE, no indexes on `dedup_key` / `link_normalized` | Full table scan per item per cycle |
| R7 | Full download every interval, gofeed default UA, unbounded body | Wasted bandwidth; some hosts (Reddit etc.) block "Gofeed" UA; memory risk |
| R8 | `interval_minutes <= 0` (web POST or config) → `time.NewTicker` panics, loop dies | RSS silently stops |
| R9 | Cross-feed ordering: newEntries reversed as one list, not sorted by date | Mixed-feed announcements out of order |

### Fixes
1. **`last_seen` column** (`ALTER TABLE seen_news ADD COLUMN last_seen DATETIME`, existing migration style in `NewDatabase`), plus
   `CREATE INDEX IF NOT EXISTS` on `dedup_key` and `link_normalized`.
2. Replace `NewsItemDuplicate` body with a single
   `UPDATE seen_news SET last_seen=CURRENT_TIMESTAMP WHERE guid=? OR dedup_key=? OR link_normalized=?`
   (empty strings passed as `nil` so they never match) and return `RowsAffected()>0`. One indexed query both checks dup and
   marks the row as still-in-feed. `MarkSeen` sets `last_seen` too.
3. `CleanupPerSource`: add `AND COALESCE(last_seen, added_at) < datetime('now','-2 days')` to the delete → rows still in
   a live feed are never pruned (fixes R1). Web settings handler calls `CleanupPerSource` instead of `Cleanup`; delete
   the now-unused `Cleanup` (fixes R2).
4. `fetchMu sync.Mutex` on `Fetcher`; `Fetch`/`Backfill` use `TryLock` and return if a cycle is already running (R3).
5. Announce loop rework in `Fetch`:
   - Collect new entries per feed; if a feed returned items and **none** were known (new feed / wiped DB), mark all seen
     silently and announce only the newest 3 (`seedAnnounce` const) (R4).
   - Sort all new entries by `PubDate` ascending (R9).
   - Global per-cycle cap `maxAnnouncePerCycle = 10`: leftovers are **not** marked seen, so they go out next cycle.
   - Before each broadcast check `bot.IsConnected()`; if false, stop without marking the rest (R5). Order becomes
     shorten → broadcast → MarkSeen (announce disabled: shorten → MarkSeen only).
6. New `fetchFeed(url)` helper replacing `fp.ParseURL`: `http.NewRequest` with `User-Agent: botIAask/<meta.Version>`,
   `If-None-Match`/`If-Modified-Since` from an in-memory `map[url]validators` (no DB — restart just does one full fetch);
   304 → feed OK, zero items; body wrapped in `io.LimitReader(…, 10<<20)`; `fp.Parse(body)` (R7). Used by Fetch and Backfill.
7. Validation: `validateRSS` in `config/validate.go` (`enabled && interval_minutes <= 0` → error, mirroring
   `validateGitHubTracker`); web RSS POST rejects `interval_minutes <= 0` with 400 (R8).

Skipped: parallel feed fetching (sequential is fine for a handful of feeds with the 30 s timeout — add a small worker pool
if the feed list grows past ~20).

## Part 2 — General security / performance
All findings below were checked in code (the top ones re-checked by me).

### Security (fix all)
| Sev | Where | Problem | Fix |
|-----|-------|---------|-----|
| **High** | `web/templates/app.js:1872-1876` | Bookmarks table uses `innerHTML` with raw `b.nickname` / `b.url`; `!bookmark ADD <url> <nick>` from any IRC user → **stored XSS** on the dashboard (can drive the admin API via `/api/csrf-token`) | Escape with existing `financeEscapeHtml` (app.js:1114); new `safeHref(u)` helper returns `#` unless `http:`/`https:` |
| **High** | `web/logs_api.go:266` + stream handler, `logger/channel_key.go:19` | Non-`#` channel param maps to the bot's **PM log** (contains live `/upload?token=` links); `/api/logs/history` & `/api/logs/stream` are public | Reject channels not starting with `#`/`&` in both handlers; stop logging the token-bearing PM line (`irc/bot.go:1385`) or redact `token=` in the logger |
| Med | same handlers | `network` param joined into path unchanged → `../` traversal to any `*_YYYY-MM-DD.log` | Accept only configured network names |
| Med | `app.js:2384-2387` | RSS `n.Title` / `n.Link` / `n.ShortLink` raw into `innerHTML` / `href` (feeds can carry `<`, `javascript:`) | Same escape + `safeHref`. While there, sweep other `innerHTML` templates rendering IRC/feed/user text (GitHub events, todos, pastes) and escape any raw field |
| Med | `web/server.go:3850, 3888`, `web/progtodo_api.go:69/90/135` | Mutating handlers without CSRF check | Add `csrfValid(r, sessionToken)` (they already resolve the session via `staffAdminFromRequest`) |
| Med | ~25 `json.NewDecoder(r.Body)` incl. unauthenticated `/api/login` (server.go:2499) | Unbounded bodies | Wrap once in the mux: middleware applying `http.MaxBytesReader(w, r.Body, 1<<20)` to non-upload routes (upload routes keep their own limit) |
| Med | `web/server.go:259` | `http.Server` without timeouts (slowloris) | `ReadHeaderTimeout: 10s`, `IdleTimeout: 120s`; no `WriteTimeout` (SSE streams) |
| Med | `web/rate_limiter.go:92` | With `trust_forwarded_for`, uses **leftmost** XFF (client-controlled) → login rate-limit bypass | Use the **rightmost** XFF entry (the one the trusted proxy appended) |
| Low | whole web server | No security headers; `/f/` serves uploader's Content-Type | Same middleware sets `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`. Strict CSP skipped: dashboard uses inline `onclick` everywhere — add when those are migrated |
| Low | `uploads/db.go:357` | Cancel-by-token ignores status → can cancel approved uploads | `AND status = 'pending_form'` |
| Low | `web/server.go:2580` | Logout's `DeleteCSRFToken(sessionToken)` deletes nothing | Delete CSRF row by session token |
| Low | `web/auth_db.go:262/272` | Password change doesn't revoke other sessions | Delete other sessions for that user on change |

Skipped (low value): username-enumeration timing on unknown user; periodic sweep of expired session rows (lookups already reject them).

### Performance
| Sev | Where | Problem | Fix |
|-----|-------|---------|-----|
| Med | `web/server.go:3286-3335` `/api/finance` | Data race on `forexCache*` fields; cache expiry → every in-flight request refetches upstream; uses uncached `irc.FetchRates` | Mutex around the cache fields + `golang.org/x/sync/singleflight` if already in go.sum, else the mutex alone held across the refresh; switch to `cachedFetchRates` |
| Med | `crypto/db.go:31, 218` | `crypto_prices` never pruned, `MAX(fetched_at)` full scans on `/api/health` etc. | Index on `fetched_at`; delete rows older than 30 days after each fetch |
| Med | `web/server.go:792` `/api/changelog` | Public, uncached; each hit calls GitHub API or spawns `git log` | 5-minute in-memory cache |
| Low | `bookmarks/db.go:315` | `ORDER BY timestamp` without index | `CREATE INDEX IF NOT EXISTS` on `timestamp` |

Skipped: crypto chart N+1 (already behind a 10-min cache), logs catalog caching — add if the logs dir grows large.

## Part 3 — Admin login dialog redesign (web/templates/index.html, app.js, style.css)

Current state: inputs reuse `.btn .btn-ghost` classes (buttons styled as inputs), no `<form>` (Enter doesn't submit,
password managers don't recognise it), labels not linked, error div is empty (shows a blank red line), 429 rate-limit
message never shown, no loading state, "Security Portal / Authorize" copy.

Plan:
- Replace `#login-modal` with a real `<form id="login-form" novalidate>`: brand header (lucide `shield-check` icon in a
  gradient badge using `--brand-gradient-*`, "Admin sign in", one-line subtitle), `<label for>`-linked fields with
  `autocomplete="username"` / `"current-password"`, `required`, password show/hide toggle (lucide `eye`/`eye-off`),
  Caps Lock hint, `role="alert"` error area, full-width submit with inline spinner, text-style Cancel.
- New `.field`, `.input`, `.auth-card`, `.auth-badge` rules in style.css using existing tokens (`--bg-card`,
  `--glass-border`, `--primary`, `--primary-glow`, `--error`) so light/dark themes both work; mobile: card goes
  full-width with 16px gutter.
- `login()` in app.js: `preventDefault`, disable button + spinner, show server text for 401 ("Invalid username or
  password") and 429 (server message), clear password on failure/close, autofocus username on open, Escape and
  overlay-click close, reset state in `hideLogin()`. Keep `window.login/showLogin/hideLogin` exports.
- Apply the same `.input` / form treatment to `#force-password-modal` (the second step of the login flow, same
  ugliness), with `autocomplete="new-password"`.
- Use the `frontend-design` skill guidance while implementing.

## Verification
- `go build . && go vet ./... && go test ./... && go test -race ./rss/ ./web/`
- New tests in `rss/`: (a) feed with 60 items, retention 50 → second `Fetch` announces 0 (R1); (b) empty DB + 40-item feed →
  exactly 3 broadcasts (R4); (c) 15 new items → 10 announced, remaining 5 next cycle (cap/deferral); (d) disconnected
  bot mid-loop → unannounced items not marked; (e) `httptest` server returning 304 with ETag → no parse, feed OK.
  Use existing fake bot pattern in `rss/fetcher_lifecycle_test.go`.
- Config test: RSS enabled with `interval_minutes: 0` fails validation.
- Web tests (httptest, existing patterns in `web/csrf_flow_test.go`): `/api/logs/history?channel=nick` and
  `network=../x` → 400; progtodo/upload-public POST without CSRF → 403; oversized login body → 413; response carries
  `nosniff`; `GetClientIP` with `X-Forwarded-For: spoof, real` → `real`.
- Browser: bookmark with nickname `<img src=x onerror=alert(1)>` renders as text.
- Implementation order: Part 1 (RSS) → Part 2 (security, then perf) → Part 3 (login UI); build+test after each part.
- Login UI: run `go run main.go -dashboard`, open http://localhost:3366, check with Playwright in light + dark themes and
  at 390px width: Enter submits, wrong password shows message, 6 bad attempts shows 429 text, password manager attrs present,
  Escape closes, successful login proceeds (and force-password modal still appears when required).
