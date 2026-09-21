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
| `github/` | GitHub repo activity tracking (pushes/PRs/releases via the Events API), PAT encryption, IRC announcements |
| `crypto/` | CoinGecko price fetching, market history DB |
| `stats/` | Activity tracking, SQLite persistence |
| `bookmarks/` | URL bookmark DB |
| `logger/` | Channel event logging, log rotation, `ChannelActivity` (log parsing for `!chanstats`) |
| `flight/` | OpenSky/AirLabs flight tracking |
| `weather/` | Open-Meteo weather data |
| `omdb/` | OMDB movie database lookups |
| `progtodo/` | Programmer TODO list DB |
| `db/` | Shared SQLite connection pooling and PRAGMA helpers |
| `meta/` | Version, name, author constants |
| `internal/ircusage/` | Command usage tables; source for `-usage`, `!help` and `!help <cmd>` (alphabetical, `Names`/`Lookup`) |
| `internal/sysinfo/` | System info (gopsutil wrapper) |

---

## Configuration

All configuration lives in `config/config.yaml` (active) and `config/config.yaml.template` (reference). The config is YAML-only — no `.env` file. Some API keys fall back to environment variables (see `config/config.go`).

**Secrets at rest**: `config/secrets.go` AES-256-GCM-encrypts every secret in `config.yaml` (IRC services/SASL password, channel keys, `web.auth.password`, `flight.api_key`, `omdb.api_key`, `github.token`) as `enc:<base64>`, using `data/github_secret.key` (same key as GitHub PATs). `LoadConfig` decrypts into memory; `SaveConfig` encrypts a copy. Plaintext values are accepted and auto-migrated (file rewritten encrypted) on next load. To change a secret by hand, write it in plaintext and restart. New secret field → add it to `mapSecrets`. Lose the key = re-enter secrets. Also encrypted: `services.nickserv_password` and `services.client_cert` (PEM cert+key).

**IRC network auth** (per network, dashboard-only — no IRC commands, by design): SASL PLAIN or EXTERNAL (`services.mechanism`, needs `use_ssl` + client cert); NickServ `IDENTIFY` on connect when SASL is off and a NickServ password is stored; dashboard "Network authentication" panel does NickServ register/identify (`web/irc_auth.go`, `irc/nickserv.go`) and generates/uploads/removes the client cert (only its SHA-256 fingerprint is ever returned). With `bot.debug: true`, irc-go logs raw sent lines, including IDENTIFY/REGISTER passwords — keep debug off in production.

**Config reload** at runtime: send `SIGHUP` to the process, call `!rehash` in IRC, or use the `/api/rehash` web endpoint.

---

## Databases

All SQLite databases are created at first run in `data/`. Each subsystem owns its own file:

| File | Owner |
|------|-------|
| `rss_seen.db` | `rss` package |
| `stats.db` | `stats` package (`bot_stats` snapshots + `chan_activity` per-channel daily rollups for web Channel Stats, built from `logs/` by `stats.RunChanRollup`; `logger.ParseDayLog` is the parser) |
| `bookmarks.db` | `bookmarks` package |
| `uploads.db` | `uploads` package |
| `crypto.db` | `crypto` package |
| `prog_todos.db` | `progtodo` package |
| `web_auth.db` | `web` package (auth, sessions, CSRF tokens) |
| `github_seen.db` | `github` package (event dedup, per-repo ETag cache) |

The `db/` package provides shared `OpenDB()` with connection pooling and WAL-mode pragmas.

Schema migrations use `ALTER TABLE ADD COLUMN` with "duplicate column" error tolerance — errors are logged but don't abort startup.

`data/github_secret.key` (not a DB) is a random AES-256 key auto-generated on first run, encrypting GitHub PATs stored in `config.yaml` (`github_tracker.repos[].token_encrypted`). Back it up alongside `data/` — losing it makes stored tokens unrecoverable (re-enter them via the dashboard; public repos are unaffected).

### GitHub Tracker: token scope for private repos

The polling loop calls only `GET /repos/{owner}/{repo}/events` (`github/client.go`) and parses `PushEvent`, `PullRequestEvent`, `ReleaseEvent`, `IssuesEvent` (opened/closed only), `CreateEvent`, `DeleteEvent` (branch/tag create/delete) from that single response (`github/events.go`). Announcement tags show the natural GitHub identifier per kind (`[PUSH]` with the short SHA colored in the body, `[PR #N]`, `[RELEASE <tag>]`, `[ISSUE #N]`, `[BRANCH <name>]`/`[TAG <name>]`), and links are shortened via the same `rss.ShortenURLWithService` helper RSS uses (`config.RSS.url_shortener`) before being broadcast. For a **public** repo no token is needed (or use a `public_repo`-scope classic PAT just for the higher rate limit). For a **private** repo, create the token at https://github.com/settings/tokens and enter it via the dashboard or `!gh add ... --private` (never hand-edit `token_encrypted`):

- **Classic PAT**: `repo` scope (full control of private repositories — GitHub has no finer-grained classic scope that covers push+PR+release+issues events).
- **Fine-grained PAT**: select the specific repo(s) and grant, read-only:
  - **Contents** — covers push/commit events, releases, and branch/tag create/delete
  - **Pull requests** — covers PR events
  - **Issues** — covers `IssuesEvent` and the `!gh search` issue-number lookup fallback
  - **Metadata** is mandatory and auto-included; nothing else is required.

`event_types` per repo now accepts `push`, `pull_request`, `release`, `issues`, `create`, `delete` (empty means all of them).

`!gh search <owner>/<repo> <query>` makes **separate, on-demand** calls outside the polling loop: a numeric query tries `GET .../pulls/{number}` then falls back to `GET .../issues/{number}`; a 7–40 char hex string calls `GET .../commits/{sha}`; anything else searches the `seen_events` history locally (regex/text match over already-announced events, no live API call). These on-demand calls don't share `Fetch()`'s rate-limit backoff — a burst of searches has no protection of its own (known limitation).

### GitHub Tracker: IRC admin commands

`!gh list` / `!gh add <owner>/<repo> [network:#chan ...] [--private]` / `!gh del <owner>/<repo>` / `!gh search <owner>/<repo> <query>` (admin + `!admin` session required, see `irc/github_admin.go`). Adding a repo without `--private` mirrors the dashboard's add flow (validate, save, rehash, best-effort channel join) with no token. `--private` is the **only** way to attach a token from IRC, and it's deliberately narrow:

- Refused outright if invoked in a channel — a PAT must never be typed where it can be logged by every client/bouncer in the room.
- In a PM, the bot first `WHOIS`es the admin and waits (5s) for numeric `671` (`RPL_WHOISSECURE`) before offering the flow. Not every ircd sends this numeral even over a real TLS connection — the check is intentionally fail-closed (no `671` = treated as unverifiable = refused, pointing the admin at the web dashboard instead), so `!gh add --private` may always refuse on less mainstream networks even when the connection actually is encrypted.
- If secure, the bot asks the admin to reply with the PAT in that same PM within 120s; the reply is intercepted before normal PRIVMSG logging/`!seen`/`!tell` processing so the plaintext token never touches `logs/`.
- Editing an existing repo's token is still dashboard-only; IRC only adds/removes whole repo entries.

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
3. Add usage text in `internal/ircusage/print.go` (rows are sorted automatically; `!help` and `!help <cmd>` read them — add an optional intro in `about` in `lookup.go` for commands needing explanation).
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
