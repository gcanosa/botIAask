# Multi-network: close the remaining gaps

**Status: implemented 2026-09-08.** Phases 1, 3, and 4 are complete with regression
tests (`go build`, `go vet`, `go test ./...`, and `go test -race ./...` all pass).
Phase 2's backend (per-network stats schema, tracker, live stream) is complete and
tested; the dashboard's network-selector UI and `channel_admins` rendering were
**deferred** — the data is already flowing over `/api/stats/history`,
`/api/stats/stream`, and `/api/status`, but no chart control consumes it yet. Also
deferred: a per-network rehash endpoint (Phase 3) and `!quit` network-scoping
(Phase 4) — both are design choices rather than bugs, left for a follow-up
decision. The case-fold network-name lookup gap (Phase 4) was judged low-value/
high-risk to fix and was left as-is.

## Context

botIAask connects to several IRC networks at once: one process-wide `*Bot`
(`irc/bot.go:38`) owning N `*ircNetwork` connections (`irc/network.go:23`), which
embed `*Bot` so global methods are promoted. Multi-network landed in v0.4.1; an
uncommitted follow-up (per-network `admins`, an `enabled` flag, the web scope
selector) is already on disk and passes build/vet/tests.

An audit of `irc/`, the persistence layers, and `web/` found the foundations are
solid — `logger/`, `bookmarks/`, `uploads/`, admin sessions, `tellPending` and RSS
announce targets are correctly network-keyed; legacy single-network configs still
load; the dashboard can add/remove/edit networks. But there is a tail of confirmed
bugs, several serious, plus real feature gaps.

**The headline problem: upgrading an existing single-network deployment silently
loses data.** The rebuild migrations copy `network = ''` (`bookmarks/db.go:221`,
`:263`) and every read path filters `WHERE network = ?` with the real name, so
`!seen` history disappears, undelivered `!tell`s are never delivered, and timed
reminders are destroyed (`irc/social.go:211` calls `b.network("")`, which returns
nil, sending the reminder down the "demote to on-join" branch where it is then
invisible).

Decisions taken with the user: implement everything **including per-network
stats**; **keep** the cross-network channel-name guard (`config/validate.go:40`);
**auto-backfill** legacy rows only when exactly one network is configured.

Goal: multi-network is correct, upgrades are lossless, per-network activity is
attributable, and every per-network config field is reachable from the dashboard.

---

## Phase 1 — Data loss and correctness (do first)

### 1.1 Backfill legacy `network = ''` rows

New exported helper in `bookmarks/` and `uploads/`, called from `main.go` after
config load and DB open, given the resolved default network name — and only when
`len(cfg.IRC.Networks) == 1`. Otherwise log a warning naming the affected tables
and leave rows alone (attribution would be a guess).

```
UPDATE seen      SET network = ? WHERE network = '';
UPDATE tells     SET network = ? WHERE network = '';
UPDATE reminders SET network = ? WHERE network = '';
UPDATE bookmarks SET network = ? WHERE network = '';   -- watch UNIQUE(network,url)
UPDATE uploads   SET network = ? WHERE network = '';
```

`bookmarks` needs care: `UNIQUE(network, url)` means a legacy `('', url)` row can
collide with an existing `('libera', url)`. Use `UPDATE OR IGNORE` then delete the
leftover `''` rows, so the newer row wins.

Run it inside the existing migration transaction style in `bookmarks/db.go`
(`migrateSeenNetworkKey`, `migrateBookmarksNetworkKey` are the pattern to follow —
`PRAGMA table_info` guard, transactional, idempotent).

### 1.2 Independent of the backfill, make the read paths tolerant

- `irc/social.go:211` — `b.network(r.Network)` → `b.networkOrDefault(r.Network)`
  (`irc/network.go:110`). This alone stops legacy timed reminders being destroyed
  and is worth doing regardless of 1.1.

### 1.3 `aiRequests++` takes the wrong mutex (data race)

`irc/bot.go:1423` sits in `handleCommand`, whose receiver is `*ircNetwork`
(`irc/bot.go:476`), so `b.statsMu` binds to `ircNetwork.statsMu`
(`irc/network.go:31`, depth 0) — **not** `Bot.statsMu` (depth 1) — while
`b.aiRequests` is a `Bot` field. Two networks serving `!ask` take different locks,
and it races `GetAIRequestCount` (`irc/bot.go:338`).

Fix: `b.Bot.statsMu`, matching `sendAdminStats` (`irc/bot.go:1687`), which already
spells it correctly. Add a `-race` regression test driving `!ask` accounting from
two `ircNetwork` values concurrently.

### 1.4 `!join` can write a config the bot won't start with

`addIRCChannelToConfig` (`irc/bot.go:263`) appends and calls
`persistIRCChannelsToDisk` → `config.SaveConfig` (`config/config.go:273`), which
does **no validation**. `LoadConfig` *does* validate (`config/config.go:221`) and
`main.go:98` is `log.Fatalf`. So `!join #dup` on network B, when `#dup` is on
network A, bricks the next start.

Fix: call `config.ValidateConfig` inside `SaveConfig` and return the error. Every
caller already handles a save error (`irc/bot.go:277` reports it to IRC; the 15 web
handlers return 500). `!join` then refuses with a clear message instead of
corrupting config. This also protects the other 14 save paths.

While here: make `SaveConfig` atomic — write to `path + ".tmp"`, then `os.Rename`.
A crash mid-write currently truncates the config.

### 1.5 Cross-network authorization via nick collision

- **`!todo`** — `programmer_todos` has no `network` column (`progtodo/db.go:41`);
  `DeleteByAuthor` matches `author_nick` alone (`progtodo/db.go:153`). Taking the
  nick `bob` on another network lets you list and delete `bob`'s TODOs.
  Add `network TEXT NOT NULL DEFAULT ''` via the existing `ALTER TABLE`
  convention, thread `b.name` through `Add`/`ListByAuthor`/`DeleteByAuthor`
  (`irc/bot.go:1163, 1179, 1190, 1217`), and backfill per 1.1.
  Note `web/progtodo_api.go:125` writes web usernames into the same namespace —
  give web-origin rows a reserved sentinel network (e.g. `"web"`) so they can't be
  claimed by an IRC nick.
- **`!download`** — `ListApprovedFilesByUser` (`uploads/db.go:489`, called from
  `irc/bot.go:1373`) has no network predicate though the column exists. Add
  `AND COALESCE(network,'') = ?`.
- **`!bookmark add` quota** — `CountUserBookmarksSince` (`bookmarks/db.go:369`)
  is nick-global; add the network predicate. Same for `FindBookmarksByURLContains`
  (`bookmarks/db.go:341`, used by `!bookmark find` at `irc/bot.go:987`).

### 1.6 Network registry can unregister a live connection

`runNetwork` (`irc/network.go:193`) deletes `b.networks[name]` unconditionally on
loop exit. `ApplyLiveConfig`'s endpoint-change path (`irc/network.go:552`)
disconnects then immediately spawns a replacement; if the new goroutine registers
first, the old goroutine's delete erases the live entry and `b.network(name)`
returns nil forever — `SendMessage`, `Broadcast`, `NotifyAdmins` and
`NetworkStatuses` all report a working network as gone.

Fix: compare-and-delete, and `defer` it so a panic recovered by `guard.Go`
(`internal/guard/guard.go:12`) can't leave a stale entry either:

```go
defer func() {
    b.networksMu.Lock()
    if b.networks[netCfg.Name] == n { delete(b.networks, netCfg.Name) }
    b.networksMu.Unlock()
}()
```

### 1.7 Uncancellable initial-connect retry

`connectNetwork` retries forever (`irc/network.go:495`) with no shutdown channel and
registers unconditionally on success. Removing or disabling an unreachable network
doesn't stop its loop; when the server returns it re-registers itself despite being
gone from config. Repeated add/remove cycles leave two goroutines for one name,
both with live callbacks — **every command answered twice**.

Fix: give `ircNetwork` a `quit chan struct{}`; `disconnectNetwork`
(`irc/network.go:577`) closes it; the retry loop selects on it between attempts;
`runNetwork` checks it before registering. Track pending (not-yet-connected)
networks in a second map so `disconnectNetwork` can reach them.

---

## Phase 2 — Per-network stats

Currently `bot_stats` (`stats/db.go:38`) has no network column, every counter in
`stats.Tracker` (`stats/stats.go:20-28`) is process-global, and `t.users` is a
bare-nick set — so the same nick on two networks counts once, and two different
people sharing a nick also collapse into one. `channel_admins` is already computed
with correct `network:#chan` keys (`stats/stats.go:232`) and shipped at
`web/server.go:370`, but **nothing renders it**.

- **Schema** — `ALTER TABLE bot_stats ADD COLUMN network TEXT NOT NULL DEFAULT ''`
  in `migrateStatsSchema` (`stats/db.go:65`), following the existing
  duplicate-column-tolerant convention. Add `Network` to `StatEntry`
  (`stats/db.go:16`) and to the INSERT/SELECT lists (`stats/db.go:104, 112, 137`).
- **Tracker** — replace the flat counter fields with
  `map[string]*windowCounters` keyed by network; key `users` by
  `network + "\x00" + nick` (the `adminSessionKey` convention). Add `network` as
  the first parameter of `LogMessage`/`LogAction`/`LogJoin`/`LogPart`/
  `LogAdminCommand`/`LogFailedAuth`/`LogAIRequest` (`stats/stats.go:128-199`) and
  thread `n.name`/`b.name` through the ~15 call sites in `irc/network.go` and
  `irc/bot.go`.
- **Snapshot** — `snapshot()` (`stats/stats.go:238`) emits one row per network per
  interval instead of one row total.
- **Query** — `GetStatsSince`/`GetRecentStats` take an optional network filter;
  with none, sum rows sharing a timestamp so the existing dashboard chart keeps
  working unchanged.
- **Web** — `/api/stats/history` and `/api/stats/stream` (`web/server.go:2100`,
  `:2136`) accept `?network=`; add a network selector above the activity chart,
  defaulting to "All networks". Render `channel_admins` now that it exists.

Keep the aggregate view as the default throughout — this adds a dimension, it does
not change what the dashboard shows by default.

---

## Phase 3 — Web UI and config gaps

- **`quit_message` is silently deleted on every UI network edit** —
  `web/server.go:1982` assigns it unconditionally, but the JS PUT body
  (`web/templates/app.js:2577-2584`) never sends it. Make `QuitMessage` a
  `*string` and assign only when non-nil, matching the `if req.SASL != nil` guard
  one line below. Same treatment for `Enabled` (`web/server.go:1977`) — safe from
  the browser today, unsafe from any other client since nil means "enabled".
  Then add a quit-message input to the network form.
- **SASL is unreachable from the UI** — the API accepts `sasl`
  (`web/server.go:1826, 1947`) but no inputs exist, so every network added from the
  dashboard has SASL disabled and must be finished by hand in YAML. Add
  enabled/username/password fields (password write-only, masked like channel keys).
- **`tls_skip_verify`** — the only `IRCNetworkConfig` field with zero UI exposure.
  Add it to `ircNetworkRow` (`web/server.go:1201`) and both request structs, plus a
  checkbox with a clear warning label.
- **Add-network path can't set `channels`/`admins`/`tls_skip_verify`**
  (`web/server.go:1845`) — accept them so a network can be created complete.
- **Legacy bare `rss.channels` entries** — `web/server.go:1415, 1538, 1638` and
  `irc/bot.go:855, 873` match only `net:#chan`, while `Broadcast`
  (`irc/network.go:658`) still honours bare entries. The toggle reads OFF while the
  bot announces; turning it ON double-announces; turning it OFF doesn't remove it.
  Fix in `config/rss_channels.go`: give `RSSChannelContainsFold` and
  `SetRSSChannelAnnounce` a `defaultNetwork` and compare via `SplitNetworkChannel`
  (`config/rss_channels.go:50`) instead of raw string equality.
- **`!unignore`** — `!ignore` (`irc/bot.go:600`) has no inverse and the list is
  memory-only. Add `!unignore <nick>` deleting `adminSessionKey(b.name, user)`,
  register it in the admin command list (`irc/bot.go:678`), and add usage text in
  `internal/ircusage/print.go:58`. Make `!stats`' ignore count per-network
  (`irc/bot.go:1697`) to match its per-network admin count.
- **Per-network rehash** — every per-network mutation calls
  `runFullRehashFromWeb` (`web/server.go:1177`), which re-diffs all networks and
  NOTICEs on all of them. Add a per-network reconnect endpoint so a single network
  can be bounced without touching the others.

---

## Phase 4 — Reporting accuracy and polish

- `/api/health` `irc_connected` and `/api/status` `connected`/`irc_authenticated`
  are "ANY network" booleans (`irc/network.go:628`, `:639`), so 1-of-3 networks
  down still reads healthy and green (`web/templates/app.js:592`, `:682`). Report
  `connected_networks`/`total_networks` and make the badge degrade to a warning
  state when some but not all are up.
- Clear `authenticated` in the disconnect callback (`irc/network.go:483`) — it is
  currently never reset, so "Identified" can be stale.
- Rate limiter: key on `network + sender + target` (`irc/bot.go:1848`). The channel
  guard stays, but **PM targets still collide** (target = the bot's nick, often
  identical across networks), so the guard is not load-bearing here. Also evict
  entries older than the window — `limits` currently grows without bound.
- Re-prime `tellPending` in `ApplyLiveConfig` — `loadPendingTells`
  (`irc/social.go:40`) runs once from `Start()`, so a network added at runtime never
  delivers its pre-existing tells.
- `!ticket approve` (`irc/bot.go:1282`) should notify on the requester's network
  using the row's `Network`, as the web path does (`web/server.go:3093`).
- Fold case in the `b.networks` map lookup (`irc/network.go:102`) — every diff
  helper folds, so a case-only rename makes all lookups miss.
- Include `TLSSkipVerify` (and `Bot.Debug`) in the reconnect trigger
  (`irc/network.go:550`); both currently need a restart.
- `!quit` (`irc/bot.go:633`) quits every network and leaves the daemon alive with
  no connections and no reconnect path — either scope it to the current network or
  have it shut the process down.
- `web/server.go:1582` discards the `ok` from `FindIRCNetworkByName`, returning a
  confusing "Channel not found" for an unknown network; return "Unknown network"
  like its siblings (`:1629`, `:1688`).
- Delete the dead `Bot.adminEnabled` field (`irc/bot.go:41`).
- Update `index.html:726` — it still claims admin-hostmask changes skip the rehash
  NOTICE, but the handler now calls `runFullRehashFromWeb` (`web/server.go:1306`).
- Document `enabled`, `admins`, and `tls_skip_verify` in
  `config/config.yaml.template`, and note in CLAUDE.md that `SaveConfig` destroys
  YAML comments on the first dashboard save.

---

## Critical files

`irc/network.go`, `irc/bot.go`, `irc/social.go` · `stats/stats.go`, `stats/db.go` ·
`bookmarks/db.go`, `progtodo/db.go`, `uploads/db.go` · `config/config.go`,
`config/rss_channels.go`, `config/config.yaml.template` · `web/server.go`,
`web/templates/app.js`, `web/templates/index.html` · `main.go`

Reuse rather than reinvent: `adminSessionKey`/`splitAdminSessionKey`
(`irc/network.go:87`) for composite keys, `networkOrDefault` (`irc/network.go:110`)
for legacy-empty fallback, `config.SplitNetworkChannel`/`JoinNetworkChannel`
(`config/rss_channels.go:50`) for RSS targets, `config.FindIRCNetworkByName`
(`config/irc_config.go:90`), `bookmarks.IRCCaseFoldNick` for nick folding, the
`migrateSeenNetworkKey` rebuild pattern (`bookmarks/db.go:193`) for schema changes,
and `guard.Go` for every goroutine.

---

## Verification

1. `go build ./... && go vet ./... && go test ./... && go test -race ./...` —
   `-race` specifically must catch 1.3 before the fix and pass after.
2. **Upgrade test (the important one):** copy a pre-multi-network `bookmarks.db`
   (or build one with the legacy schema), add a `!tell`, a timed reminder and
   `!seen` rows, then start the new binary with a single network. Assert
   `!seen <nick>` still answers, the tell is delivered on join, and the reminder
   fires — all of which fail today.
3. **Two-network functional test.** Point two network blocks at a local ergo (or
   two ports on one), same nick on both. Verify: `!todo add` on A is invisible to
   the same nick on B; `!download` likewise; admin on A's `admins` list is not
   admin on B; `!ignore` on A doesn't silence on B; `!unignore` works; PM rate
   limiting on A doesn't throttle B.
4. **Rehash churn.** With one network's server unreachable, `!rehash` after
   removing it, then re-add it — confirm no duplicate replies (Phase 1.7) and that
   `NetworkStatuses` stays accurate through an endpoint change (Phase 1.6).
5. **Config safety.** `!join #dup` where `#dup` is on another network must be
   refused, and `config.yaml` must still load afterwards.
6. **Dashboard.** Edit a network that has a `quit_message` and confirm it survives;
   create a network with SASL and `tls_skip_verify` entirely from the UI; confirm
   the stats chart defaults to the aggregate and filters correctly per network; take
   one network offline and confirm the health badge degrades instead of staying
   green.
