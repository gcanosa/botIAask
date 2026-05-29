# botIAask — Developer Guide

## Project Overview

botIAask is a feature-rich IRC bot written in Go. It connects to IRC via `ergochat/irc-go`, answers questions via an OpenAI-compatible LLM endpoint (LM Studio by default), and exposes an optional web dashboard on port 3366.

**Binary:** `go build .` produces `./botIAask`  
**Go version:** see `go.mod` (currently Go 1.26)  
**Run:** `go run main.go` (foreground) or `go run main.go -dashboard` (daemon + web UI)

---

## Package Map

| Package | Purpose |
|---------|---------|
| `main` (root) | Entry point, daemon lifecycle, signal handling |
| `ai/` | OpenAI-compatible HTTP client wrapping `go-openai` |
| `irc/` | IRC connection, command dispatch, all bot commands |
| `config/` | YAML config load/save, struct definitions |
| `web/` | HTTP server, dashboard handlers, auth, CSRF |
| `uploads/` | Paste and file upload DB + disk storage |
| `rss/` | RSS feed fetching, deduplication, IRC announcements |
| `crypto/` | CoinGecko price fetching, market history DB |
| `stats/` | Activity tracking, SQLite persistence |
| `bookmarks/` | URL bookmark DB |
| `logger/` | Channel event logging, log rotation |
| `flight/` | OpenSky/AirLabs flight tracking |
| `weather/` | Open-Meteo weather data |
| `omdb/` | OMDB movie database lookups |
| `progtodo/` | Programmer TODO list DB |
| `db/` | Shared SQLite connection pooling and PRAGMA helpers |
| `meta/` | Version, name, author constants |
| `internal/ircusage/` | IRC help text formatter |
| `internal/sysinfo/` | System info (gopsutil wrapper) |

---

## Configuration

All configuration lives in `config/config.yaml` (active) and `config/config.yaml.template` (reference). The config is YAML-only — no `.env` file. Some API keys fall back to environment variables (see `config/config.go`).

**Config reload** at runtime: send `SIGHUP` to the process, call `!rehash` in IRC, or use the `/api/rehash` web endpoint.

---

## Databases

All SQLite databases are created at first run in `data/`. Each subsystem owns its own file:

| File | Owner |
|------|-------|
| `rss_seen.db` | `rss` package |
| `stats.db` | `stats` package |
| `bookmarks.db` | `bookmarks` package |
| `uploads.db` | `uploads` package |
| `crypto.db` | `crypto` package |
| `prog_todos.db` | `progtodo` package |
| `web_auth.db` | `web` package (auth, sessions, CSRF tokens) |

The `db/` package provides shared `OpenDB()` with connection pooling and WAL-mode pragmas.

Schema migrations use `ALTER TABLE ADD COLUMN` with "duplicate column" error tolerance — errors are logged but don't abort startup.

---

## Web Dashboard Auth

- Session cookies: `admin_session` (HttpOnly, Secure, SameSite=Strict, 24h TTL)
- CSRF: every mutating request (POST/PUT/PATCH/DELETE) must include `X-CSRF-Token` header or `csrf_token` form field. The CSRF token is returned at login.
- `requireAdminCSRF(r)` — use this instead of `checkAuth(r)` for any handler that mutates state.
- `csrfValid(r, sessionToken)` — use this when session is already validated separately (e.g. `staffAdminFromRequest` handlers).
- Initial admin password: if `web.auth.password` is empty in config, a random 32-char hex password is generated and printed once to the log.

---

## AI Client

`ai/client.go` wraps the OpenAI SDK with a 90-second HTTP timeout (`lmStudioTimeout`). The bot passes an IRC-specific system prompt that enforces terse, plain-text responses.

---

## Adding a New IRC Command

1. Add a handler method on `*Bot` in `irc/bot.go` (or a new file in `irc/`).
2. Register it in the command dispatch map in `bot.go`.
3. Add usage text in `internal/ircusage/print.go`.
4. If it fetches external data, set an explicit HTTP client timeout.

---

## Security Notes

- All DB queries use parameterized statements — no string concatenation in SQL.
- File downloads validate `ContentPath` is within the uploads/pastes directory using `pathWithinDir`.
- `GetClientIP(r, trustForwarded bool)` — only pass `true` when behind a trusted reverse proxy (`web.trust_forwarded_for: true` in config).
- Rate limiting for logins: 5 attempts / 15 minutes per IP (`web/rate_limiter.go`).

---

## Development Workflow

```bash
# Build
go build .

# Test all packages
go test ./...

# Race detector
go test -race ./...

# Vet
go vet ./...
```

Logs go to `logs/<server>_<channel>_<date>.log` (IRC channel events).  
Runtime data (DBs, PID file) lives in `data/`.
