# Comprehensive Audit: Safety, Security, Performance & New Features

## Context

Full audit of the botIAask IRC bot (~16,200 lines, 88 Go files). The goal is to fix all security vulnerabilities, eliminate bugs, improve performance and code hygiene, update documentation, and propose new features for the next release.

---

## Phase 1 — Critical Security Fixes

### 1A · Remove plaintext password from stdout
**File:** `web/auth_db.go:105`

**Current:**
```go
fmt.Printf("Initial admin account created: %s / %s (Change required on first login)\n", username, password)
```
**Fix:** Never print the password value. Log only the username:
```go
log.Printf("Initial admin account created for user %q (change password on first login)", username)
```

---

### 1B · Remove hardcoded `"admin"/"password"` fallback
**File:** `web/auth_db.go:88-94`

When `cfg.Web.Auth.Password == ""`, generate a cryptographically random password with `crypto/rand` instead of falling back to `"password"`. Print the generated password exactly once, clearly marked as a one-time credential to save:

```go
if password == "" {
    b := make([]byte, 16)
    if _, err := rand.Read(b); err != nil {
        return fmt.Errorf("failed to generate initial admin password: %w", err)
    }
    password = hex.EncodeToString(b)
    log.Printf("[SECURITY] Generated initial admin password for %q: %s — save this, it will not be shown again", username, password)
}
```

---

### 1C · AI HTTP client missing timeout
**File:** `ai/client.go:24, 36`

Both `NewClient` and `UpdateConfig` use `&http.Client{}` with no timeout. A hung LM Studio request blocks the goroutine indefinitely.

Add a package-level constant and use it in both places:
```go
const lmStudioTimeout = 90 * time.Second

// In NewClient and UpdateConfig:
config.HTTPClient = &http.Client{Timeout: lmStudioTimeout}
```

---

## Phase 2 — High Security Fixes

### 2A · CSRF validation missing on all mutating endpoints
**File:** `web/server.go` (20+ handlers)

`validateCSRFAndGetSessionToken` exists but is called by **zero** handlers. All state-changing handlers only call `checkAuth(r)`.

**Fix:** Add a `requireAdminCSRF` helper:
```go
func (s *Server) requireAdminCSRF(r *http.Request) (bool, bool) {
    isAdmin, needsChange := s.checkAuth(r)
    if !isAdmin {
        return false, false
    }
    if r.Method == http.MethodPost || r.Method == http.MethodPut ||
        r.Method == http.MethodPatch || r.Method == http.MethodDelete {
        if _, ok := s.validateCSRFAndGetSessionToken(r); !ok {
            return false, false
        }
    }
    return isAdmin, needsChange
}
```

Replace `checkAuth` with `requireAdminCSRF` in all mutating handlers:

| Handler | Method |
|---|---|
| `handleRSSToggle` | POST |
| `handleRSSFetchNow` | POST |
| `handleRehash` | POST |
| `handleConfigIRCAdmins` | POST/DELETE |
| `handleIRCChannels` | POST/DELETE |
| `handleIRCChannelAnnounce` | PUT/PATCH |
| `handleIRCChannelAutojoin` | PUT/PATCH |
| `handleIRCChannelSession` | PUT/PATCH |
| `handleRSSSettings` | POST |
| `handleStatsToggle` | POST |
| `handleUsers` | POST/PATCH/DELETE |
| `handlePasswordUpdate` | POST |
| `handleWeatherSettings` | POST |
| `handleLoggerSettings` | POST |
| `handleAISettings` | POST |
| `handlePasteDelete/Approve/Reject` | POST/DELETE |
| `handleUploadSettings` | POST |
| `handleRSSNews` DELETE branch | DELETE |

Also confirm the frontend JS sends `X-CSRF-Token` on all fetch/XHR mutation calls before merging.

---

### 2B · Path traversal in paste/file download handlers
**File:** `web/server.go:2269, 2780`

`pathWithinDir()` exists at line ~1956 but is **not called** before `os.ReadFile(upload.ContentPath)` (paste view) or `os.Open(upload.ContentPath)` (file download).

**Step 1** — add public accessor in `uploads/db.go` next to `FilesDiskDir()`:
```go
func (d *Database) PastesDiskDir() string { return d.pastesDir }
```

**Step 2** — in `handlePasteView` before `os.ReadFile`:
```go
if !s.pathWithinDir(upload.ContentPath, s.uploadsDB.PastesDiskDir()) {
    http.Error(w, "Invalid content path", http.StatusInternalServerError)
    return
}
```

**Step 3** — in `handleFileDownload` before `os.Open`:
```go
if !s.pathWithinDir(upload.ContentPath, s.uploadsDB.FilesDiskDir()) {
    http.Error(w, "Invalid file path", http.StatusInternalServerError)
    return
}
```

---

## Phase 3 — Medium Security & Bug Fixes

### 3A · `GetClientIP` ignores `TrustForwardedFor` config
**File:** `web/rate_limiter.go:87`

Change signature to accept the trust flag:
```go
func GetClientIP(r *http.Request, trustForwarded bool) string
```
Skip XFF/X-Real-IP headers when `trustForwarded == false`. Update the single call site in `handleLogin`:
```go
GetClientIP(r, s.getConfig().Web.TrustForwardedFor)
```

---

### 3B · `forexCache` missing mutex
**File:** `web/server.go:58-59`

`forexChartCache` is protected by `forexChartMu` but `forexCache` and `forexUpdate` have no mutex, yet `handleFinance` can be called concurrently.

Add `forexMu sync.Mutex` to the `Server` struct alongside `forexChartMu`. Wrap all reads/writes to `forexCache` and `forexUpdate` in `handleFinance` with `s.forexMu.Lock()/Unlock()` (same pattern used for `forexChartMu`).

Run `go test -race ./web/...` to confirm.

---

### 3C · Migration errors silently ignored
**Files:** `crypto/db.go:41-42,55,70-71` and `web/auth_db.go:65-68`

Replace `_, _ = sqldb.Exec("ALTER TABLE ...")` with proper error handling that ignores "duplicate column" (expected on re-run) but logs genuine errors:
```go
if _, err := sqldb.Exec("ALTER TABLE crypto_prices ADD COLUMN change_24h REAL DEFAULT 0"); err != nil {
    if !strings.Contains(err.Error(), "duplicate column") {
        log.Printf("crypto_prices migration warning: %v", err)
    }
}
```
Apply to all 5 sites in `crypto/db.go` and 4 sites in `web/auth_db.go`.

---

### 3D · Expired session/CSRF token cleanup
**File:** `web/auth_db.go`

Add a background cleanup goroutine to `AuthDatabase`:
```go
func (a *AuthDatabase) StartCleanup(interval time.Duration) {
    go func() {
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for range ticker.C {
            now := time.Now()
            a.db.Exec("DELETE FROM web_sessions WHERE expires_at < ?", now)
            a.db.Exec("DELETE FROM csrf_tokens WHERE expires_at < ?", now)
        }
    }()
}
```
Call `authDB.StartCleanup(6 * time.Hour)` from `NewServer` after `CheckAndSeedInitialAdmin`.

---

### 3E · Channel members map leaks on part/config reload
**File:** `irc/bot.go` (inside `ApplyLiveConfig` or equivalent)

When the bot's channel list changes via rehash, old channel keys in `channelMembers` are never removed. After applying new config channels, prune keys not in the new config:
```go
newMembers := make(map[string]map[string]struct{})
for _, ch := range newCfg.IRC.Channels {
    if existing, ok := b.channelMembers[ch.Name]; ok {
        newMembers[ch.Name] = existing
    } else {
        newMembers[ch.Name] = make(map[string]struct{})
    }
}
b.channelMembers = newMembers
```

---

## Phase 4 — Low Priority Fixes

### 4A · IPv6 normalization in rate limiter
**File:** `web/rate_limiter.go`

Add a `normalizeIP` helper and call it at the top of `IsAllowed`:
```go
func normalizeIP(raw string) string {
    if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
        return ip.String()
    }
    return raw
}
```
This ensures `::1`, `0:0:0:0:0:0:0:1`, and `::0001` hit the same bucket.

---

### 4B · Remove development scratch file
```
git rm scratch/inspect_rss.go
```
This is a development tool file that was committed to the repo.

---

## Phase 5 — Docker / Infrastructure

### 5A · Add HEALTHCHECK to Dockerfile and docker-compose.yml

The `/api/health` endpoint already exists (`handleHealth`). Wire it up:

**Dockerfile** (before final `CMD`):
```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q --spider http://localhost:3366/api/health || exit 1
```

**docker-compose.yml** (under the service):
```yaml
healthcheck:
  test: ["CMD", "wget", "-q", "--spider", "http://localhost:3366/api/health"]
  interval: 30s
  timeout: 5s
  retries: 3
  start_period: 15s
```

---

## Phase 6 — Documentation

### 6A · Create `CLAUDE.md` in project root
Initialize with architecture overview, package responsibilities, dev setup, and key design decisions (IRC command prefix, multi-DB approach, rehash mechanism, IRC hostmask auth).
Use `/init` skill or write manually.

### 6B · Update README.md
- Add new commands added since last release
- Document `--validate-config` once Feature 3 ships
- Add Prometheus metrics section once Feature 2 ships

---

## Phase 7 — Recommended New Features (next release)

### Feature 1 · `--validate-config` CLI flag (effort: 0.5–1 day)
**Files:** `main.go`, `config/config.go`

Add `-validate-config` flag: load config, run `ValidateConfig(cfg)`, print all errors, exit 0 on success or 1 on failure. Enables CI/CD pre-deploy checks.

`ValidateConfig` should check: IRC server/port/nick non-empty; web port in 1–65535 range; AI base URL is valid if AI enabled; upload max size positive; retention days non-negative. Return `errors.Join(errs...)` listing all problems at once.

---

### Feature 2 · Session revocation from admin dashboard (effort: 2–3 days)
**Files:** `web/auth_db.go`, `web/server.go`, dashboard templates

Add to `AuthDatabase`:
- `ListActiveSessions(userID int) ([]SessionInfo, error)` — returns token prefix (never full token), created_at, expires_at
- `RevokeSession(tokenID string) error`
- `RevokeAllSessionsForUser(userID int) error`

Add `GET/DELETE /api/sessions` endpoints (admin-only for other users; any user for own sessions). Add a table to the Users dashboard page with a Revoke button per session row.

---

### Feature 3 · Prometheus metrics endpoint (effort: 2–3 days)
**Files:** `web/server.go`, new `web/metrics.go`

Add `github.com/prometheus/client_golang` to `go.mod`. Register metrics:
- `botiaask_irc_connected` (gauge 0/1)
- `botiaask_uptime_seconds` (gauge)
- `botiaask_ai_requests_total` (counter)
- `botiaask_active_sessions` (gauge)
- `botiaask_web_requests_total{path,method,status}` (counter via middleware)
- `botiaask_rss_items_total` (counter from RSS DB)

Gate with `web.metrics_enabled: false` config flag. Register `mux.Handle("/metrics", promhttp.Handler())`.

---

## Verification Checklist

| Fix | Test |
|---|---|
| 1A/1B | Start with empty `web.auth.password`; grep stdout for the literal word "password" value |
| 1C | `httptest.NewServer` with 200s sleep handler; confirm timeout error in <95s |
| 2A | POST to each mutating endpoint without `X-CSRF-Token`; expect 401/403 |
| 2B | Insert row with `ContentPath = "../../etc/passwd"`; confirm 500 on paste/file view |
| 3A | Send request with `X-Forwarded-For: 1.2.3.4`, `RemoteAddr: 10.0.0.1`; confirm returns `10.0.0.1` when trust=false |
| 3B | `go test -race ./web/...` with concurrent `handleFinance` calls |
| 3C | Fresh DB + existing DB migration — no panics, no spurious errors |
| 3D | Insert expired sessions; wait one tick; confirm deleted |
| 3E | Populate `channelMembers` with removed channel; call `ApplyLiveConfig`; assert key absent |
| 4A | Same IPv6 address in different notations hits same rate-limit bucket |
| 5A | `docker build` succeeds; `docker inspect` shows HEALTHCHECK |

---

## Execution Order

1. **PR 1** — Fixes 1A + 1B + 1C (30 min, zero risk, highest impact)
2. **PR 2** — Fix 2B path traversal (isolated, no refactor)
3. **PR 3** — Fix 2A CSRF enforcement (largest change; verify JS sends `X-CSRF-Token` first)
4. **PR 4** — Fixes 3A–3E (medium fixes, can batch or split)
5. **PR 5** — Fixes 4A + 4B + Phase 5 Docker (housekeeping)
6. **PRs 6–8** — New features (after all security fixes merged)
