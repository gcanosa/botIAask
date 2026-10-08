# Revamp admin `!stats` (colorful, readable, richer)

## Context
`!stats` (admin + `!admin` session only) already exists: `sendAdminStats` in `irc/bot.go:1748`. It emits 4 dense
lines of `k=v | k=v` plain text — hard to scan. Goal: same command, same privilege, but grouped, colored
and with a few genuinely useful extra numbers. No new command, no new tables, no rename.

## Layout (5 short lines, one topic each)
Colored label chip `[STATS]`-style (reuse the `\x03fg,01` on-black convention from `weather_cmd.go`
consts `ircBold/ircGreen/ircYellow/ircRed/ircGray/...`; move-free: they're same package `irc`).
Labels bold-cyan, values white/bold, on/off green/red, separators gray `·`.

1. **BOT**    `v0.4.3 · net=libera · ● online · up app 3d 4h · sess 2h · go1.26 · 41 goroutines`
2. **ACTIVITY** (new, from `tracker.GetHistory(since=24h, network)`): `msgs 1,204 · actions 37 · AI 58 · joins/parts 12/9 · peak users 84` — omitted if tracker disabled ("activity snapshots off" in gray).
3. **CHANNELS** (new): `12 joined · 214 users online · ign 3 · admins 1` (counts from existing `netCfg().Channels`, ignore/login maps already computed; users-online from the network's channel-presence state if cheaply available, else skip).
4. **DATA**    `queue 2 · bookmarks 130 · reminders 4 · pastes 55 · files 21 · news rows 9,801` (existing counters; `-1` → gray `n/a`, non-zero queue highlighted yellow/red).
5. **SERVICES + HOST** `RSS on(announce on) · GitHub on 3 repos · linux/arm64 · CPU 12% · RAM 41% (3.2G free) · heap 48MB`
   (new: GitHub tracker on/off + repo count from cfg; heap via `runtime.ReadMemStats`).

Thresholds: CPU/RAM green <60, yellow <85, red else (small helper `pctColor`).

## Implementation (single file, ~80 lines net)
- Refactor `sendAdminStats` into: data gathering (unchanged reads) → small pure formatters
  `statsLabel(name)`, `statsKV(k,v)`, `statsOnOffC(bool)`, `pctColor(float)`; then `sendPrivmsgMentionedLines(target, sender, l1..l5)`.
  Put helpers + formatting in new `irc/stats_fmt.go` (keeps bot.go from growing); keep `statsOnOff`/`statsIntOrNA` (reused).
- Thousands separators via tiny helper (`fmt.Sprintf` + manual grouping; no dep).
- Keep each line < `ircTextBudget` (color codes count toward the 512 limit — verify lengths).
- Update `internal/ircusage/print.go` `!stats` description to mention the new sections; CLAUDE.md untouched.

## Reuse
- Color consts: `irc/weather_cmd.go:15-30`. Counters: `uploadsDB.Count*`, `bookmarksDB.*`, `rssDB.CountSeenNews`.
- `stats.Tracker.GetHistory` (`stats/stats.go:372`) + `mergeStatEntries` semantics: entries are per-interval snapshots, so sum Messages/Actions/AIRequests/Joins/Parts and take max UserCount.
- `sysinfo.Collect`, `formatDuration`.

## Verification
- `go build . && go vet ./... && go test ./...` (add one small `stats_fmt_test.go`: `pctColor` thresholds, thousands grouping, every produced line ≤ budget).
- Manual: run bot, `!admin` login in PM, `!stats`; eyeball colors in a client; confirm non-admins still get nothing.

## Open point (not blocking)
Public per-channel stats (top talkers, busiest hour, etc.) would need new per-user/per-channel tracking —
deliberately out of scope; can be a separate `!chanstats` later.
