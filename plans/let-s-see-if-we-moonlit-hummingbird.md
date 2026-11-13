# Reddit subreddit tracker → IRC announcements

## Context
The admin wants to follow subreddits the way the bot follows RSS feeds: new posts announced to
IRC once each, oldest first, with per-subreddit choice of `network:#chan` targets, managed from
the dashboard (and `!reddit` in IRC). Decisions so far: public `.rss` (no API key), every new post
(`/new`), `!reddit add/del/list` IRC admin commands, NSFW skipped by default.

The RSS feature has a single global `rss.channels` list, so the **per-entry channels** model is
taken from the GitHub tracker (`github_tracker.repos[].channels`), and the fetch/dedup/announce loop
from `rss/fetcher.go`.

## Config (`config/reddit.go`, new)
```yaml
reddit:
  enabled: true
  interval_minutes: 10        # default 10, min 2
  subreddits:
    - name: golang            # bare name, validated ^[A-Za-z0-9_]{2,21}$ (no "r/", no URL)
      channels: ["libera:#go", "SekurNet:#news"]
      nsfw: false             # allow NSFW sub; default false
```
- `RedditConfig` / `RedditSub` added to `config.Config` (`yaml:"reddit,omitempty"`), defaults in
  the same place as `applyRSSDefaults`, `FindRedditSub` (case-insensitive).
- Validation in `config/validate.go` next to `validateGitHubTracker`: name regex, duplicates,
  channels parsed with `config.SplitNetworkChannel` and checked against configured networks
  (reuse whatever `validateGitHubTracker` does for its channels).
- Add a commented example to `config/config.yaml.template`.

## Package `reddit/` (new)
- **`fetcher.go`** — `Fetcher` copied in shape from `github/fetcher.go`: `Start/Stop/SetEnabled/
  IsEnabled/ApplyConfig` (restart only when the interval changes), `fetchMu.TryLock`, the
  wait-for-IRC loop, `SubStatuses()` (last fetch time / error per sub) for the dashboard.
  Same `BotInterface{Broadcast, IsConnected}` (`*irc.Bot` already satisfies it).
  - Per sub, sequentially: `GET https://www.reddit.com/r/<name>/new/.rss?limit=25`, UA
    `botIAask/<meta.Version> (IRC bot)` (Reddit 429s generic UAs), 15s client timeout, body via
    `io.LimitReader` into `gofeed.NewParser().Parse` (gofeed is already a dependency).
    A 429 / 403 marks the sub's status and skips the rest of this cycle (backoff to next tick).
  - Post id = Atom entry id (`t3_xxxx`), link = `https://redd.it/<id>` (Reddit's own short link,
    so no shortener call), author from `entry.Author`, time from `Published`.
  - Collect unseen entries of all subs → sort by published ascending → cap 10 per cycle (rest next
    cycle) → for each: stop if IRC dropped (leave unseen, like RSS), `bot.Broadcast(sub.Channels,
    line)`, `MarkSeen`, `time.Sleep(announcePace)` (the loop is a background goroutine, not an IRC
    handler, same as RSS).
  - **First poll of a new sub**: mark everything seen, announce only the newest 3 (same rule as RSS).
- **`db.go`** — `data/reddit_seen.db` via `db.OpenDatabase`: `seen_posts(sub, post_id PK, title,
  link, author, published, added_at)`; `IsSeen`, `MarkSeen`, `HasAny(sub)` (first-poll check),
  prune rows older than 30 days per sub. The `/new` window is 25 posts, so 30 days can't re-announce.
- **`format.go`** — `FormatLine`: `[REDDIT][r/golang] 15:04 - Title (u/author) 🔗 https://redd.it/abc`
  in the same mIRC colors as `rss.FormatIRCNewsLine`; strip control chars from title/author
  (a title can't inject IRC formatting or `\r\n`).
- **`nsfw.go`** — `CheckSub(name) (over18 bool, err error)` via `/r/<name>/about.json`
  (`httpx.DecodeJSON`). Used only when adding: NSFW sub + `nsfw:false` → refused with a hint;
  if Reddit blocks the check, the add is allowed and logged.
  `// ponytail:` note: the .rss feed has no per-post NSFW flag, so NSFW posts inside a SFW sub
  are not filtered; needs the OAuth JSON API if it matters.

## Wiring
- `main.go`: build `reddit.NewFetcher(cfg, bot, db)` next to the GitHub fetcher, `SetEnabled`
  from config, call `ApplyConfig` in the rehash func where the RSS/GitHub fetchers are refreshed,
  pass to `web.NewServer` (+1 param, same as `ghf`) and `bot.SetRedditFetcher` (same as
  `SetGitHubFetcher`).
- `CLAUDE.md`: package map row, `reddit_seen.db` in the DB table, short Reddit section.

## Dashboard (`web/reddit_api.go`, new + `web/templates`)
- Routes in `web/server.go` `newServeMux`:
  - `POST /api/reddit/toggle` — on/off (copy `handleRSSToggle`), plus a Feature Control switch.
  - `GET|POST|DELETE /api/reddit/subs` — list (with per-sub status), add, remove;
    `PATCH /api/reddit/subs/edit` — channels / nsfw. All behind `requireAdmin`, every config
    change through `mutateConfig` (rollback on validation/save error), then the live
    `ApplyConfig`. Structure follows `handleGitHubTrackerRepos` / `handleGitHubTrackerRepoEdit`.
  - `GET|POST /api/reddit/settings` — interval.
- Card "Reddit" in the dashboard next to the GitHub tracker card: add field (sub name + NSFW
  checkbox), table of subs with channel checkboxes for every configured `network:#chan`
  (reuse the GitHub card's channel picker JS), last fetch / error, delete button.
  All Reddit text via `financeEscapeHtml()` / `safeHref()` / `jsq()`.

## IRC (`irc/reddit_admin.go`, new)
`!reddit list | add <sub> [network:#chan ...] [--nsfw] | del <sub>` — admin + `!admin` session,
dispatch next to `!gh` in `irc/bot.go`. Without channels, `add` uses the current channel.
Edits through the same config-save + rehash path `commitGHRepoAdd`/`ghDel` use. List output
capped at `maxListLines`. Usage row in `internal/ircusage/print.go` (+ `about` intro in `lookup.go`).

## Tests
- `reddit/fetcher_test.go` with an `httptest` server serving an Atom fixture and a fake bot:
  no repeats across two cycles, oldest-first across two subs, first poll announces only 3,
  cap 10 with remainder next cycle, nothing marked seen while IRC is down, 429 stops the cycle.
- `reddit/format_test.go`: control chars stripped.
- `config`: validation (bad name, URL as name, unknown network, duplicate).
- `web`: add/delete/edit round-trip and NSFW refusal (stub `CheckSub` via a package var).
- `irc`: `!reddit add/list/del` with `newCaptureNetwork`.

## Verification
1. `go vet ./... && go test -race ./...`
2. Run `go run main.go -dashboard` with a test network, add `r/golang` to a test channel from the
   dashboard: 3 newest posts appear, the next cycle (or "fetch now") shows nothing new.
3. `!reddit add programming #test`, `!reddit list`, `!reddit del programming` from IRC.
4. Add a known NSFW sub without the checkbox → refused.
5. `!rehash` / SIGHUP with an edited interval → loop restarts, no re-announcements.

Skipped: OAuth/JSON (score, per-post NSFW, hot/top) — add if Reddit keeps blocking the VPS IP or
filtering by score is wanted. Reddit posts aren't shown in the public News panel; add if wanted.
