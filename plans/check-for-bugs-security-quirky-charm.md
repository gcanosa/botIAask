# Bug / security / performance sweep (follow-up to 8f546db)

## Context
User asked for a full check for bugs, security issues, and perf wins. Baseline: `go vet` clean,
`go test -race ./...` passes, staticcheck shows only minor items. Three read-only audits (web,
irc, data/fetchers) found ~44 candidates; the ones below were re-verified against the code.
Low-value / by-design items are listed at the end as skipped.

Approach: smallest diff per fix, one regression test per non-trivial fix, one commit per tier.

---

## Tier 1 — High (secret leaks, privilege escalation, data loss)

1. **Admin hostmask substring match → admin takeover.** `irc/bot.go:393`, `irc/network.go:113`
   use `strings.Contains(source, admin)`; `x!~ethernet@user/ethernet2` matches `~ethernet@user/ethernet`.
   Fix: one shared helper `matchAdmin(source, entry)` = `strings.EqualFold` on full source, or
   `HasSuffix(lower(source), "!"+lower(entry))` / `"@"+lower(entry)` (anchored; still matches the live
   config entry `~ethernet@user/ethernet`). Test: lookalike user/host/IP-prefix rejected, real one accepted.

2. **API keys leak to IRC via `*url.Error`.** `flight/fetch.go:252-266`, `omdb/fetch.go:61-77` return
   `client.Do` errors whose text includes `?api_key=`/`?apikey=`; `irc/flight_cmd.go`, `irc/movie_cmd.go`
   print them publicly. Fix: on `Do` error, return `fmt.Errorf("request failed: %w", ue.Err)` (unwrap
   `*url.Error`, drop URL). Test: fake unreachable host, assert key not in error string.

3. **Secrets in logs when debug on (default!).** `main.go:37` `-debug` defaults to `true`, overriding
   config; ircevent logs raw IDENTIFY/AUTHENTICATE lines; `irc/network.go:451` logs PRIVMSG content
   *before* `tryConsumePendingGitHubToken` (PAT logged). Fix: flag default `false` (config `bot.debug`
   still works); move the debug PRIVMSG log after the token-consume check. Also set template
   `debug: false`.

4. **Encryption key silently regenerated on any read error.** `config/secrets.go:38`, `github/crypt.go:26`
   regenerate+overwrite `data/github_secret.key` on *any* error or wrong length (e.g. trailing newline,
   EACCES) → all `enc:` secrets and PATs lost. Fix: regenerate only on `errors.Is(err, fs.ErrNotExist)`;
   otherwise return an error (wrong length → error naming the file). Write new key with `O_EXCL`. Test.

5. **PM log readable anonymously via legacy-key fallback.** `web/logs_api.go:290`
   `ChannelFileKey("#irc.libera.chat","")` → `irc.libera.chat` = the PM log (contains `/upload?token=` links).
   Fix: drop the fallback key when it equals any configured network name (or when the bare channel name
   contains a `.` matching a network) — simplest: skip `keys[1]` if `cfg` has a network with that Name.
   Test.

6. **Stored XSS via RSS feed image URL.** `web/templates/app.js:271` uses `JSON.stringify(raw)` in an
   HTML attribute; `"` breaks out. Fix: `src="${financeEscapeHtml(new URL(raw).href)}"`
   (have `isSafeImageSrc` return normalized href).

7. **Upload token race → many full-size orphan files.** `web/server.go:2831` checks status once,
   `uploads/db.go:339-348` UPDATEs without status guard. Fix: add `AND status = 'pending_form'` to the
   UPDATEs, check `RowsAffected()==1`, and on 0 delete the just-written file and return 409. Test.

## Tier 2 — Medium

8. **`/upload` paste branch has no body cap.** `web/middleware.go:19` exempts `/upload`; paste path
   calls `FormValue` unbounded. Fix: in the paste branch wrap `r.Body` with `MaxBytesReader` (paste max,
   e.g. 1 MiB) before parsing.
9. **Forced password change only enforced in UI.** `requireAdminCSRF` returns `needsChange` but callers
   ignore it. Fix: inside `requireAdminCSRF`/`staffAdminFromRequest`, return not-admin when
   `needsChange` (except the password-update + csrf-token + logout handlers, which use their own checks).
10. **Keyed (+k) channel logs public; SSE `Access-Control-Allow-Origin: *`.** `web/logs_api.go`,
    `web/server.go:984,1018`. Fix: `validLogTarget` rejects channels configured with a key unless
    the request has an admin session; remove the `*` CORS header (same-origin dashboard doesn't need it).
11. **Compress double-click deletes the file.** `web/server.go:4001-4010`. Fix: per-ticket
    `sync.Map` TryLock-style guard (return 409 if in progress); on `os.Remove(oldPath)` failure don't
    delete `newPath` if it's the only copy (`os.IsNotExist` → continue).
12. **Bookmark rate limit never fires** (UTC `CURRENT_TIMESTAMP` text vs local `time.Time` binding).
    `bookmarks/db.go:378`. Fix: bind `since.UTC().Format("2006-01-02 15:04:05")`. Test.
13. **`SaveConfig` not concurrency/crash safe.** `config/config.go:332`. Fix: package-level mutex in
    `SaveConfig`, `os.CreateTemp` in same dir, `f.Sync()` before rename.
14. **Commands sent by PM reply to the bot itself.** `irc/network.go:483` passes raw `target`. Fix:
    `_, reply := seenTargets(target, sender)` already computed — pass `reply` to `dispatchCommand`
    (gh-add-private PM flow keys on target; verify it still sees a non-channel target).
15. **`!tell` flood → bot Excess Flood kill.** `irc/social.go:112`. Fix: cap pending tells per
    target (e.g. 10) and per sender/hour in `bookmarks` insert; pace delivery 500ms/line; truncate via
    existing `sanitize`.
16. **Command goroutines unbounded + no rate limit on `!weather/!flight/!movie/!crypto/!chanstats`.**
    `irc/bot.go:486`. Fix: `TryAcquire`-style — if `cmdSem` full, drop (don't spawn parked goroutine);
    apply existing rate limiter to those commands; key limiter by `network+nick` not target.
17. **Live config mutated in place from IRC** (`irc/bot.go:159,292-314,884`) racing readers; in-place
    `Channels[:0]` filter. Fix: copy-on-write — clone the network's `Channels` slice before
    append/filter, and route IRC-side saves through the same lock as web (`SaveConfig` mutex from #13
    covers the file; slice clone covers the race).
18. **Rehash: no lock, drops CLI overrides.** `rehash_apply.go:34`. Fix: `sync.Mutex` around
    `doApplyRehash`; re-apply the CLI overrides (`-dashboard`, `-news`, `-debug`) to the reloaded
    config (store them in a small struct in main).
19. **GitHub tracker double-announce + zero-interval panic + shared deadline starvation.**
    `github/fetcher.go:79,207,220`. Fix: `fetchMu.TryLock` like rss; `if interval<=0 {interval=5}`;
    per-repo `context.WithTimeout` instead of one for the loop.
20. **Log rotation deletes source after failed gzip; holds global lock throughout.**
    `logger/logger.go:206`. Fix: check `gz.Close()`/`out.Close()` errors before `os.Remove`; compress
    outside `mu` (only today's file is ever open for writing).
21. **Uploader IP uses leftmost XFF.** `web/server.go:2767`. Fix: call existing `GetClientIP(r, cfg.Web.TrustForwardedFor)`.
22. **Private reminders/todos keyed by nick only.** Fix (lazy): `!reminder read/list/del` and
    `!todo list/del` require the requester to be identified (account tag / `IsAuthenticated`) or reply
    by NOTICE to sender only, never to channel. Defer full account-binding.

## Tier 3 — Low / performance

23. **Crypto market history inserted ~56× per point.** `crypto/db.go:60`. Fix: unique index on
    `(gecko_id, timestamp_ms)` (dedupe existing rows first in migration), `INSERT OR IGNORE`. Big
    DB/size + chart perf win.
24. **Public `/api/pastes` loads all rows, pages in Go.** `uploads/db.go:408`. Fix: `LIMIT ? OFFSET ?` + `COUNT(*)`.
25. `regexp.MatchString` in loop → precompiled var (`uploads/pasteclassify.go:148`).
26. Panics: `"\x01"` one-byte CTCP (`irc/network.go:463`, add `len(message)>=2`); `!bc 5 % 0.5`
    (`irc/calc.go:72`, check `int64(right)==0`).
27. Approve without status guard (`uploads/db.go:369`, add `AND status='pending_approval'`).
28. RSS same item in two feeds announced twice in one cycle (`rss/fetcher.go:352`): dedupe by GUID/link
    in the collected batch.
29. CoinGecko `Retry-After` unbounded (`crypto/market_chart.go:66`): cap at 60s.
30. Unbounded `io.ReadAll` in URL shortener (`rss/fetcher.go:521`) and currency decode: `io.LimitReader` 1 MiB.
31. Missed `rows.Err()` checks (rss/db.go, stats/db.go, bookmarks GetBookmarks, crypto GetLatestPrices,
    stats/chanrollup.go); `crypto/db.go:30` ignored CREATE error; `rss/db.go` leaked handle on init error.
32. Admin sessions survive QUIT (`irc/network.go:610`): delete from `loggedInAdmins` on QUIT.
33. CSRF token per-session overwrite breaks multi-tab: app.js clears cached token on 403 too and retries once.
34. Dead code from staticcheck U1000 in `web/server.go` (marketChartRaw*, validateCSRFAndGetSessionToken): delete.

## Skipped (by design / not worth it now)
- Login username timing (bcrypt dummy compare) — low value behind 5/15min rate limit.
- `!ping` to private hosts — already rate-limited, admin-ish utility.
- `!chanstats` for any logged channel — covered by #10's keyed-channel rule if mirrored; otherwise skip.
- channelMembers NAMES tracking, tell-pending race, ticket ID 32-bit collision, TakeTells BUSY_SNAPSHOT
  — rare, self-healing; add when observed.
- DST-offset drift in t.String() timestamp comparisons — 1h edge twice a year.

## Verification
- `go build . && go vet ./... && staticcheck ./... && go test -race ./...`
- New tests: admin match, url.Error key scrub, key-file non-ENOENT error, log fallback rejection,
  upload double-submit (2nd returns 409, 1 file on disk), bookmark rate-limit count, calc/CTCP panics.
- Manual: run `go run main.go -dashboard`, `curl '/api/logs/history?channel=%23<network>&date=<today>'`
  → 400/empty; news panel renders feed icons; `!help` via PM gets a reply.
- Commit per tier, push (matches previous sessions' workflow).
