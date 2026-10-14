# Audit follow-up: bugs, security, performance, leaks, redundant code

## Context
Full read-through audit after the Oct 6 sweep (plans/check-for-bugs-security-quirky-charm.md, already shipped) and the
newest commands (`!latest`, `!apple`, `!linux`, `!ipcheck/!hostcheck`, debug log, supervisor, short links). Baseline:
`go build ./...`, `go vet ./...` and `go test -count=1 ./...` all pass, tree clean. staticcheck/golangci-lint in
`~/go/bin` were built with an older Go and cannot read Go 1.27 export data, so they were not usable.
Every finding below was confirmed against the code (file:line). Scope choice: **safe dedupe only** for cleanup (no
handleCommand dispatch-table rewrite, no shared fetcher poller).

Approach: smallest diff per fix, one regression test per non-trivial fix, one commit per tier (cleanup in its own commit).

---

## Tier 1 — Security / data exposure

1. **`!seen` leaks private text.** `irc/network.go:611` records every message; for PMs `channel == ""` and the text is stored
   (`bookmarks/db.go:701`), then `handleSeenCommand` (`irc/social.go:197`) prints `saying: <text>` for any asker. So
   `!seen alice` in a channel reveals her last PM to the bot (e.g. `!reminder add ...`, `!todo private ...`) and anything
   said in a keyed channel. Fix: never store text when the target is not a channel; show `saying:` only when the stored
   channel equals the channel `!seen` was asked in. Also `seenTargets` (`social.go:76`) only treats `#` as a channel, so
   `&chan` is handled as a PM → use `ircChannelTarget`. Test: PM text not stored/shown; other-channel text hidden.
2. **`!ask` error leaks the LM Studio endpoint.** `irc/bot.go:1602` sends `Error contacting AI: %v`; go-openai returns the raw
   `*url.Error` (`Post "http://192.168.x.x:1234/...": dial tcp ...`) into a public channel (same class as the fixed
   flight/omdb key leak). Fix: reply `AI unavailable, try later`, log the detail only. Test with an unreachable URL.
3. **`!chanstats #other` bypasses keyed-channel privacy.** `irc/chanstats_cmd.go:~112` accepts any logged channel, including
   +k channels whose web logs are admin-only (`web/logs_api.go:isKeyedChannel`). Fix: move `isKeyedChannel` to
   `config` (shared), refuse keyed channels unless the caller is a logged-in admin.
4. **Default admin credentials in the shipped template.** `config/config.yaml.template:82-85` has `password: password`
   (`CheckAndSeedInitialAdmin` seeds it) and the Docker image copies the template as `config.yaml`, so a fresh container
   exposes `admin/password` on 0.0.0.0:3366 until first login. Fix: blank the password (random one is already generated
   and logged once), drop the unused `web.auth.enabled` (never read), placeholder `base_url`.
5. **Weak file permissions.** `data/` (0755, `main.go:149`), `data/backups` (0755, `backup.go:36`), `logs/` (0755) and
   log files (0644). `data/web_auth.db` holds bcrypt hashes + live session tokens; the PM log holds `/upload?token=` links.
   Fix: best-effort `MkdirAll(0700)` + `Chmod(0700)` on `data`, `data/backups`, `logs`, `logs/debug`; log files 0600.
6. **No password policy / no re-auth.** `web/auth_db.go:266,276,286` accept any password (even empty, which also satisfies
   the forced first-login change). `handlePasswordUpdate` (`web/server.go:2757`) doesn't ask for the current password.
   Fix: 12..72 bytes (bcrypt limit), require `current_password` unless `needs_password_change`; `RemoveUser` refuses the last
   user and self; purge expired `web_sessions`/`csrf_tokens` on start + daily.
7. **`checkAuth` ignores forced password change** in GET handlers (`handleChanStats` `web/chanstats_api.go:17`, `/api/pastes`
   client_host redaction `web/server.go:3209`). Fix: use `staffAdminFromRequest`.
8. **Public changelog exposes author e-mails** (`web/server.go:872,967`, `/api/changelog`); the dashboard JS never reads
   `.email`. Fix: drop the field.
9. **`!convert` path injection + unbounded cache.** `irc/currency.go:129,154` passes the raw user token into
   `https://api.exchangerate-api.com/v4/latest/<from>` and keys `ratesCache` on it, so `USD?a`, `USD?b`... each cause a live
   request and a permanent entry. Fix: require `^[A-Z]{3}$` for from/to; reject NaN/Inf amounts.
10. **Unauthenticated endpoints with no caps.** `/api/logs/stream` and `/api/stats/stream` (goroutine + fd + ticker per
    connection), `/api/logs/history` (scans the whole day file), `/api/logs/catalog` (ReadDir of every log per call),
    `/api/stats/history?timeframe=1y` (all rows), `/api/weather` (every request refetches when upstream is failing).
    Fix: one small `limitConns(perIP, global)` wrapper for the two SSE routes; 30s cache for catalog; tail the history file from
    the end (seek) instead of scanning; bucket `GetStatsSince` to <= 600 points in Go and default `retention_days` to 30
    when unset; 60s failure cool-down for weather (mirror `forexLastAttempt`).
11. **Upload/paste ID collisions destroy data.** `generateHex(4)` (`web/server.go:2913,3028,4142`) is a 32-bit id used as
    filename; `os.WriteFile`/`os.Create` (`uploads/db.go:333`, `web/server.go:3033`) truncate an existing file with the same id
    and the failure path then `os.Remove`s it. Ids are also the only capability for `/p/<id>` and `/f/<id>`. Fix: 6 random
    bytes, `O_EXCL` create with retry. Clamp `expires` to 0..3650 (`web/server.go:2911,3026`; `uploads/expiry.go` overflows
    `time.Duration` for huge values and marks the item instantly expired).
12. **`github.token` is sent for any IRC-supplied `owner/repo`** (`irc/latest_cmd.go:391`). A token with classic `repo` scope
    would let any user read private release/tag names. Fix: document "public_repo / no scope only" in the template and
    CLAUDE.md; log once if `X-OAuth-Scopes` contains `repo`.

## Tier 2 — Bugs / reliability

13. **The 4 command slots can be starved.** `time.Sleep` inside handlers keeps a slot: `!news` 1s x10 (`irc/bot.go:1042`, no
    rate limit), `!download` 1s x100 (`:1556`), `!ticket pending` (`:1419`), `ghList`/`ghSearchLocal`. Extra commands are then
    dropped silently. `ircevent` has no outbound flood control (verified), so lists also self-kick the bot. Fix: one paced
    sender in `sendPrivmsg`/`sendNotice` (token bucket, burst 5, ~3 lines/s, ~20 lines, no new dependency), delete the ad-hoc
    sleeps, cap list output at 10 lines + `(+N more)`, rate-limit `!news`.
14. **Self-inflicted flood and unbounded growth.** `!todo add` and `!reminder add` have no cap, and `!todo list` (`bot.go:1339`,
    `progtodo.ListByAuthor` has no LIMIT), `!reminder list` and JOIN reminder delivery (`network.go:670`) send every row
    unpaced. Fix: 25 todos / 25 reminders per nick, max 3 open `pending_form` sessions per nick for `!paste/!upload`, purge
    stale `pending_form`/`cancelled` upload rows daily (approved content is left alone).
15. **Read-loop blocking.** `deliverTells` sleeps up to 2s inside PRIVMSG/JOIN callbacks (`social.go:119`) and JOIN does a DB
    scan per join. Fix: run both through `guard.Go`; add `owner_fold` column + index to `reminders` (backfill migration,
    same pattern as `tells.to_fold`) so `ListJoinReminders`/`ListReminders` (`bookmarks/db.go:488,566`) stop loading every
    reminder of the network and filtering in Go.
16. **`updateTrackerAdmins` on every JOIN/PART/QUIT/NICK/KICK** (`network.go:657,699,725,769,803`) is O(members) with a
    lock per member (`bot.go:2014`); a netsplit stalls the read loop. Fix: call it only when the nick is a logged-in admin and
    snapshot `loggedInAdmins` once.
17. **Stale membership.** `channelMembers` is never cleared on disconnect (`network.go:807`) and keeps the channel entry after
    the bot parts/is kicked. Fix: reset on disconnect, delete on own PART/KICK.
18. **Malformed PRIVMSG panics.** `network.go:576` indexes `e.Params[1]` unguarded. Fix: `len(e.Params) < 2` return.
19. **Unguarded goroutines (a panic kills the process).** `web/server.go:1294`, `flight/fetch.go:121,128`,
    `irc/linux_cmd.go:153,191,198`, `rehash_apply.go:93,131`, `web/rate_limiter.go:26`, `logger/logger.go:136`, and
    `time.AfterFunc` callbacks (`irc/ping_cmd.go:133`, `irc/github_admin.go:258`). Fix: `guard.Go` / recover.
20. **Per-`NewServer` leaks + fatal init.** Each web disable/enable creates a new `NewAuthDatabase`, login limiter goroutine
    and short-link store + purge goroutine that are never closed (`web/server.go:148,171,176`, `web/shortlinks.go:20-35`), and
    `NewServer` calls `log.Fatalf` (kills the bot on a hot enable). Fix: `Server.Close()` called from `Stop`, limiter/purger
    get a stop channel, `NewServer` returns an error.
21. **Daemon edge cases.** (a) `runAsDaemon` returns right after `RequestQuit` (`main.go:471`) so QUIT may not flush; wait up
    to 3s like the foreground path. (b) `IsProcessRunning` is just `kill(pid,0)` (`daemon.go:67`), so a stale PID file whose pid
    was reused makes `-mode stop` SIGTERM/SIGKILL an unrelated process. Fix: hold `flock` on the PID file in the daemon and
    use it as the liveness check.
22. **`!ask`/`!news` output.** Dead truncation block (`bot.go:1610-1615`, `[:517]` can cut a rune; `sanitize` already caps at
    450 on a rune boundary) → delete. `!news` lines are the only reply not run through `b.sanitize` (`bot.go:1041`).
23. **Rate limiter.** `RateLimiter.Allow` ignores its `limit` argument (`bot.go:2057`; only `burst` is used) and five call sites
    do `b.limiter() != nil && !b.limiter().Allow(...)` (two reads, nil-deref window after a rehash disables it,
    `bot.go:817,835,845,855,1565`) and key by `target` while `rateLimited` keys by sender. Fix: route all through
    `rateLimited()`; keep today's "burst per window" semantics and say so in the template (drop the misleading `limit`).

## Tier 3 — Performance / memory

24. **`LogChannelEvent` does MkdirAll + open + write + close per line under one global mutex** (`logger/logger.go:43-107`).
    Fix: keep open handles per file (reopen on day change, close after 5 min idle), `MkdirAll` once.
25. **SQLite memory.** `cache_size(-64000)` (64 MB) x `MaxOpenConns(25)` per DB x 9 DBs (`db/sqlite.go`). Fix: -16000 and
    `MaxOpenConns(8)` (WAL: parallel readers, single writer).
26. **`GetApprovedPastes` loads every approved paste and pages in Go** on a public endpoint (`uploads/db.go:439-475`; the
    Oct 6 plan item #24 was only applied to files). Fix: store `expires_at` (unix) at approval (backfill existing rows), then
    `WHERE` + `COUNT` + `LIMIT/OFFSET` in SQL.
27. **`RollupLogs` re-parses today's and yesterday's logs for every channel every 10 minutes** (`stats/chanrollup.go:130`).
    Fix: skip files whose size+mtime are unchanged since the last pass (in-memory map).
28. **External-API commands have no cache or response caps.** `!linux` fires 11 requests per call; `!apple`, `!latest` similar.
    Decodes are unbounded (`irc/linux_cmd.go:48`, `apple_cmd.go:113`, `github/client.go:101,250`, `crypto/fetcher.go:64`,
    `crypto/market_chart.go:49`). Fix: 10-min TTL cache for `!linux`/`!apple`, 5-min for `!latest`; one
    `decodeLimited(r, max)` helper used everywhere.
29. Small: `/static/*` has no cache headers (`web/server.go:1149`) → ETag + `Cache-Control`; `!time` calls `time.LoadLocation` 16x per
    use → cache once and `import _ "time/tzdata"`.

## Tier 4 — Safe cleanup (no behavior change, separate commit)

- Delete `scratch/inspect_rss.go` (stray `package main` doing a live fetch), flight's `min` shadowing the builtin
  (`flight/fetch.go:302`), unused `web.auth.enabled`, the dead `!ask` block.
- One AES-GCM implementation: `github/crypt.go` duplicates `config/secrets.go` (key load, `writeKeyExcl`, seal/open) → Cryptor
  delegates to exported `config` helpers.
- `uploads.ircFoldNick` (`uploads/db.go:610`) is a copy of `bookmarks.IRCCaseFoldNick` → share one.
- Upload SELECT column list + `Scan` is repeated ~10x in `uploads/db.go` → one `uploadCols` const + `scanUpload`.
- `web/`: `adminOnly(handler)` wrapper for the ~60 repeated `requireAdminCSRF` blocks, `mutateConfig(fn)` helper (lock, apply,
  `SaveConfig`, roll back on error, unlock) and `findNetwork(cfg, name)` to replace the 8 hand-written index loops; then split
  `web/server.go` (4,146 lines) into topic files (server/routes, auth, irc_admin, uploads, finance, settings, sse). Pure moves.
- Rate-limit gate dedupe is covered by item 23.

## Deliberately not changing
`!ping` probing private hosts (earlier decision), nick-keyed reminders/todos (no account binding), `tls_skip_verify`
(explicit opt-in), ReDoS (Go RE2 is linear; `!gh search` regex is admin-only), `!bc` (AST evaluator, no calls), dashboard JS
(63 `innerHTML` sites reviewed; all go through `financeEscapeHtml/safeHref/jsq`), no deletion of approved-but-expired
uploads (staff can still view them; add a `purge_expired_days` setting later if wanted), no sequential-feed parallelism.

## Verification
- `go build ./... && go vet ./... && go test -race -count=1 ./...` after each tier.
- New tests (one per item): seen PM/other-channel privacy, `!ask` error masking, chanstats keyed-channel refusal, convert arg
  validation, password policy + re-auth, changelog has no email, SSE cap returns 429 past the limit, ticket id `O_EXCL`
  retry and expires clamp, pacer (N lines take >= expected time, never blocks the read loop), todo/reminder caps,
  `owner_fold` migration idempotent, `updateTrackerAdmins` not called for non-admin nicks, empty `Params` PRIVMSG, logger
  handle reuse + day rollover, `GetApprovedPastes` SQL paging matches the old Go filter, stats bucketing, perms 0700.
- Manual: `go run main.go -dashboard` with a throwaway config; `curl` `/api/stats/history?timeframe=1y` (<= 600 points),
  open 10 `/api/logs/stream` connections from one IP (11th gets 429), `ls -ld data logs` (0700), rehash web off/on and watch
  `runtime.NumGoroutine()` in `!stats` stay flat.

## Delivery
One commit per tier; after the final push, offer (do not run) the `/create-podman-dist` skill per CLAUDE.local.md.

---

## Outcome (implemented on branch `audit/security-perf-cleanup`)
All 29 items and the Tier 4 cleanup landed, one commit per tier. Differences from the plan:
- Item 18 (malformed PRIVMSG) is defensive only: ircevent already recovers panics inside callbacks.
- Added while implementing: `.gitignore` had `uploads/`, which hid every file of the `uploads` Go package except
  `db.go` (a clean clone could not build); the pattern is gone and the package is tracked.
- `rate_limiting.limit` is documented as unused rather than removed (old configs still load).
- Pacer: burst 5, one line per 500 ms (`irc/pacer.go`); `!download` now lists at most 10 files.
- Verified: build, vet, `go test -race ./...` (also in a clean clone of the branch) and a live run of the built
  binary (perms 0700, forced password change + policy, SSE cap 6 then 429, ETag 304, web off/on via SIGHUP, clean SIGTERM).
