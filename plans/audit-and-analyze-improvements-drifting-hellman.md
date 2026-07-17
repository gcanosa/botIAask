# botIAask — Audit & Improvement Plan

## Context

Full audit of code quality, performance, reconnection/reliability/stability, plus new features. The bot is in good shape overall (parameterized SQL, CSRF, batched stats, rune-safe 512-byte splitting), but the audit found real reliability defects — the worst: **every IRC command runs synchronously on the connection's read goroutine**, so one hung HTTP call (AI timeout is 90s) blocks PING/PONG and gets the bot ping-timed-out, and a rehash can leak duplicate RSS fetch loops. Scope approved by user: all fixes (Tiers 1–3) plus two features: `!convert` and `!flight` date support.

Note: the working tree already carries uncommitted work (social `!tell`/`!seen`/timed reminders, SQLite hot backups, GitHub changelog) — coherent and tested; this plan builds on top of it.

## Audit findings (summary)

**Critical reliability**
1. Blocking commands on the IRC read loop — PRIVMSG → `handleCommand` inline (`irc/bot.go:643`); `!ask` uses `context.Background()` (`:1780`) with only the 90s client timeout (`ai/client.go:13`); weather/movie/flight/currency block 10–25s each. One slow call freezes the bot → ping-timeout disconnect.
2. RSS stop-channel race → duplicate fetch loops per rehash — `Stop()`/`SetEnabled(false)` close-then-reassign `stopChan` (`rss/fetcher.go:95-121`) while the loop reads it unlocked (`:89`); the connect-wait sleep loop (`:75-80`) ignores stop entirely; `ApplyConfig` (`:138-149`) restarts every rehash.
3. `b.cfg` data race — `ApplyLiveConfig` swaps the pointer under `membersMu` (`irc/bot.go:400-416`); ~76 unlocked read sites in `irc/` plus `b.prefix`/`b.cmdName` (~84 more).
4. Bot dies permanently if initial connect fails 5× (`irc/bot.go:816-831`); `main.go` just logs. Library auto-reconnect only engages after the first success.
5. Untuned reconnect: library defaults `ReconnectFreq=2m`, `KeepAlive=4m`, `Timeout=1m` never overridden (`irc/bot.go:540`).
6. No panic recovery in background goroutines (RSS/crypto/stats/backup/reminder/`go bot.Start()`).

**Performance / correctness**
7. SQLite pragmas applied via `db.Exec` land on one pooled connection; the other up-to-24 conns have `busy_timeout=0` and FKs off (`db/sqlite.go`); `backup.go:59` same gap for `VACUUM INTO`.
8. gofeed parser has no HTTP timeout (`rss/fetcher.go`); unlocked `f.cfg` reads in `Fetch()`.
9. `stats.ApplyConfig` writes `t.cfg` without `subMu` (`stats/stats.go:116`); `connectionTime` written unlocked (`irc/bot.go:587`); `stats/db.go:132` O(n²) prepend; `currency.FetchRates` builds a new `http.Client` per call.

**Deferred (documented, not in scope)**: `handleCommand` 970-line dispatch-map refactor (ugly but harmless once off the read loop; linear prefix matching has ordering subtleties a map must re-encode); outbound flood pacing (bulky paths already sleep; worst burst ≈16 lines — leave a `// ponytail:` note in `sendPrivmsgMentionedLines`); web/server.go split; test gaps in `ai`/`logger`/`progtodo`.

## Implementation plan

Verified against ircevent v0.6.0 internals: callbacks run serially on the read goroutine; `Connection.Send` is internally locked (concurrent sends from goroutines are safe — web/RSS already do it); `Loop()` reconnects forever after the first successful `Connect()`. modernc.org/sqlite v1.49.1 parses `path?_pragma=...` and applies pragmas per pooled connection.

### Tier 1 — reliability-critical (in this order)

**Step 1. Panic-guard helper** — new `internal/guard/guard.go` (~20 lines):
```go
func Go(name string, fn func()) {
    go func() {
        defer func() {
            if r := recover(); r != nil {
                log.Printf("panic in %s: %v\n%s", name, r, debug.Stack())
            }
        }()
        fn()
    }()
}
```
Replace `go ...` at long-lived sites: `main.go:241,257,308,337,364,373,415,424` (keep the bot-error log inside the closure), `rss/fetcher.go:115`, `irc/social.go:188`, `stats/stats.go:82` (keep `runWG.Add(1)` outside), and the new dispatch goroutine (Step 3). Skip short-lived rehash goroutines (YAGNI).

**Step 2. `b.cfg`/`b.prefix`/`b.cmdName` race — before going concurrent.**
- `Bot.cfg` becomes `atomic.Pointer[config.Config]`; delete `prefix`/`cmdName` fields. Accessors: `getCfg()`, `pfx()`, `cmd()`. Mechanical replace (~154 sites, mostly `irc/bot.go`, plus `irc/social.go`, `flight_cmd.go`, `movie_cmd.go`, `weather_cmd.go`, `ping_cmd.go`, `worldtime.go`): `b.cfg.` → `b.getCfg().`, `b.prefix` → `b.pfx()`, `b.cmdName` → `b.cmd()`. `ApplyLiveConfig` uses `b.cfg.Store(newCfg)`; `GetConfig()` returns `b.cfg.Load()` without membersMu.
- `rateLimiter`: add a `limiter()` accessor under `membersMu.RLock` for the 2 bare read sites (`bot.go:1107,1762`).
- `connectionTime`: move the write at `bot.go:587` into the adjacent `statsMu.Lock()` block; wrap reads (`:1070`, `:2094`).
- Accepted residual (mark with `// ponytail:`): rare admin ops (`persistAnnounceToIRC`, `!news on/off`, join/part config edits) still mutate the live snapshot in place — the RSS fetcher deliberately shares the pointer; copy-on-write would break that for a bigger diff.
- Tests: update `irc/quit_test.go` literals to use `NewBot`; add a race test hammering `GetConfig()`/`IsAdmin()` while `ApplyLiveConfig` swaps.

**Step 3. Async command dispatch** — `irc/bot.go:643` + ~15 lines:
- `Bot.cmdSem chan struct{}` (buffered 4, set in `NewBot`).
- Replace the inline call with `b.dispatchCommand(...)`: early-return if the message lacks the command prefix (no goroutine per chat line), else `guard.Go` a goroutine that acquires `cmdSem` *inside* (read loop never blocks; bursts park cheaply) and calls `handleCommand`.
- `recordSeen`/`deliverTells` stay synchronous in the callback (fast after Step 6).
- AI call gets a real deadline at `bot.go:1780`: `context.WithTimeout(context.Background(), 60*time.Second)`; keep the 90s client timeout as backstop.
- Known behavior change: two commands from one user can interleave replies — acceptable.

**Step 4. Connect retry-forever + tuned keepalive** — `irc/bot.go`:
- Replace the 5-attempt loop (`:816-831`) with retry-forever, exponential backoff 2s→cap 2m, logging each failure; delete the error-return path (it's a daemon; giving up permanently is strictly worse).
- In the `ircevent.Connection` literal (`:540`): `ReconnectFreq: 30 * time.Second`, `KeepAlive: 60 * time.Second`, `Timeout: 30 * time.Second` (library constraint: KeepAlive ≥ Timeout).

**Step 5. RSS fetcher stop race** — `rss/fetcher.go`:
- `Start()`: capture `stop := f.stopChan` and the interval under `f.mu` once at entry; connect-wait loop selects on `stop` vs `time.After(5s)` (fixes both the unlocked read and the loop that survives `Stop()` while parked in the 2-minute wait).
- `ApplyConfig`: no-op restart when `enabled` and `IntervalMinutes` are unchanged — just swap `f.cfg` in place. Kills the leak at the source.
- Snapshot `f.cfg` once at the top of `Fetch()`/`Backfill()` (removes unlocked reads racing `SetConfig`).
- Tests: `Start()` with a disconnected stub bot exits within ~1s of `Stop()`; `ApplyConfig` with unchanged settings keeps the same `stopChan`.

### Tier 2 — performance/correctness

**Step 6. SQLite pragmas per pooled connection** — `db/sqlite.go`: move all pragmas into the DSN (`dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=cache_size(-64000)&_pragma=temp_store(2)"`), delete the `db.Exec` loop. All 7 callers inherit the fix unchanged. `backup.go:59`: append `?_pragma=busy_timeout(10000)` so `VACUUM INTO` waits instead of failing busy. Test: new `db/sqlite_test.go` — `SetMaxIdleConns(0)`, read `PRAGMA busy_timeout` twice, expect 5000 on fresh connections.

**Step 7. Stats cfg race + gofeed timeout** — `stats/stats.go`: move `t.cfg = cfg` inside the existing `subMu.Lock()` in `ApplyConfig`; lock the two unlocked reads in `snapshot()`/`maybePruneStatsHistory()`. `rss/fetcher.go`: `fp := gofeed.NewParser(); fp.Client = &http.Client{Timeout: 30 * time.Second}` at both parse sites.

### Tier 3 — minor cleanups

**Step 8.** `stats/db.go:132`: keep `ORDER BY DESC LIMIT`, append + `slices.Reverse` (drop the O(n²) prepend). Extend `stats/db_test.go` to assert order.
**Step 9.** `irc/currency.go`: package-level `var currencyHTTP = &http.Client{Timeout: 10 * time.Second}` used by `FetchRates`. No cache (YAGNI — rate-limited human commands, off the read loop after Step 3).
**Step 10.** `// ponytail:` note in `sendPrivmsgMentionedLines` (`bot.go:1920`) naming the 300ms-sleep upgrade path if the bot ever gets flood-kicked.

### Features

**Step 11. `!convert <amt> <from> <to>`** — `irc/currency.go`:
- `handleConvertCommand(target, sender, rest string)`: parse amount (float) + 3-letter codes (uppercase); reuse `FetchRates(from)` (now on `currencyHTTP`); reply `X FROM = Y TO` with the same `[CURRENCY]` color format; usage line on bad input.
- Rewrite `handleEuroCommand`/`handlePesoCommand` as thin calls over the same fetch (dedupes the parallel copies) — keep their exact output formats.
- Register `!convert` in `handleCommand` dispatch (`irc/bot.go`), add help text in `internal/ircusage/print.go` and `!help`.
- Test: parse/format unit test with a stubbed rates map (no network).

**Step 12. `!flight <IATA> <date>`** — wire the already-parsed date (`irc/flight_cmd.go:26-32,41`):
- Add `Date *time.Time` to `flight.FetchParams` (`flight/fetch.go:16`).
- In `flight.Fetch`: when `Date` is set, query AirLabs `/schedules` (`flight_iata` param) instead of `/flight` (which is real-time only), match the entry whose `dep_time` falls on that date, and normalize into the existing `Snapshot` (reuse `normalizeAirlabs` field mapping where the shapes agree; live-position fields stay empty). If no match: `Snapshot{OK:false, Error:"no data for <date> (schedules window is limited)"}`.
- `flight_cmd.go`: pass `Date: flightDate`, remove `_ = flightDate`.
- Test: extend `flight/fetch_test.go` with a `/schedules` fixture (it already fixtures `dep_time`).

## Verification

1. `go build ./... && go vet ./...`
2. `go test ./... && go test -race ./...` — the Step 2 rehash-vs-read and Step 5 stop-during-wait tests are the race proofs.
3. End-to-end (foreground run against the real network):
   - LM Studio stopped: `!ask` then immediately `!uptime` — the second replies while the first hangs; `!ask` errors at 60s; no ping-timeout.
   - `kill -HUP` 5× then `!stats` — goroutine count stable, single RSS loop, no duplicate news posts.
   - Brief network drop — reconnect within ~30–60s (was 2–4 min).
   - Start with IRC server unreachable — retries forever, connects when it appears.
   - `!convert 100 USD ARS`, `!euro`, `!peso` — correct output; `!flight AA100 2026-07-18` — schedule data or clean "no data" message.
