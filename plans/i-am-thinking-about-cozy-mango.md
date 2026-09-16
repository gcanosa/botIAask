# GitHub Repo Activity Tracker (multi-network IRC announcer)

## Context

The user wants botIAask to watch GitHub repos (commits/pushes, pull requests, releases) and announce activity to IRC channels — across multiple networks/channels — with everything configurable and persistent through the existing web dashboard: which repos to track, which channels get which repo's announcements, how often to poll, and (for private repos) a per-repo GitHub PAT stored encrypted at rest. Announcements should be nicely formatted (bold, colors, repo name, description, link).

This mirrors the shape of the existing `rss/` feature (poll external source → dedup → format → multi-network broadcast) closely enough that `rss/` serves as the structural template throughout. Three design decisions were confirmed with the user up front:

1. **Detection**: poll GitHub's repo **Events API** (`GET /repos/{owner}/{repo}/events`) once per repo per cycle, with `ETag`/`If-None-Match` so unchanged repos cost no rate-limit budget (304). One feed covers pushes, PRs, releases.
2. **Encryption**: auto-generate a random 32-byte key on first run (`crypto/rand`) at `data/github_secret.key` (0600), used directly as an AES-256-GCM key — no argon2/passphrase, since there's no human-chosen secret to strengthen. Mirrors the existing auto-generated-admin-password UX (`web/auth_db.go:79-113`).
3. **Config granularity**: one global `interval_minutes` for the whole tracker (like `RSSConfig.IntervalMinutes`); each repo entry has its own optional event-type filter.

**Important naming collision found during research**: `config.GitHubConfig` / YAML key `github:` already exist (`config/config.go:29,71-79`), used by the dashboard's *self-changelog* widget (single repo, commits API, plaintext token, `web/server.go:763-899`). This is a separate, unrelated feature — do not touch it. The new tracker uses `GitHubTrackerConfig` / YAML key `github_tracker:` to avoid collision.

## Naming decisions

| Concept | Existing (leave alone) | New |
|---|---|---|
| Go package | — | `github/` |
| Config struct | `config.GitHubConfig` (changelog widget) | `config.GitHubTrackerConfig` + `config.GitHubTrackerRepoConfig` |
| YAML key | `github:` | `github_tracker:` |
| Data files | — | `data/github_seen.db`, `data/github_secret.key` |

`config/rss_channels.go`'s `SplitNetworkChannel`/`JoinNetworkChannel` are already generic and used outside RSS (`irc/network.go:764`, `irc/bot.go:875`, `stats/stats.go:249`) — import and reuse as-is. `DefaultRSSNetwork`/`RSSChannelContainsFold`/`SetRSSChannelAnnounce` are RSS-only in call sites today; rename+move them to `config/network_channels.go` as `DefaultNetworkFor`/`ChannelListContainsFold`/`SetChannelAnnounce` (identical bodies) as a standalone first commit, since the tracker's repo-channel-toggle UI needs the same semantics. Mechanical rename, ~8 call sites (`web/server.go:1443,1449,1572,1676`, `irc/bot.go:884,885,900,901`) plus its test file.

## `github/` package

Mirrors `rss/` package shape (`rss/fetcher.go`, `rss/db.go`, `rss/irc_source.go`).

- **`github/fetcher.go`** — `Fetcher` struct with the same lifecycle as `rss.Fetcher`: `Start()` (launched via `guard.Go("github tracker", fetcher.Start)`), ticker loop on `IntervalMinutes`, `stopChan` captured once at top of `Start()` (avoids the swap race documented at `rss/fetcher.go:73-76`), `Stop()`/`SetEnabled()`, `SetConfig()` (hot-swap, no restart) vs `ApplyConfig()` (restarts ticker only if `Enabled`/`IntervalMinutes` changed — mirrors `rss/fetcher.go:154-169`), `RepoStatuses()` for the admin UI (mirrors `FeedStatuses()` at `rss/fetcher.go:187-210`), and `EncryptToken`/`DecryptToken` pass-throughs to the cryptor.
  Depends on a minimal `BotInterface{ Broadcast(channels []string, message string); IsConnected() bool }` — `*irc.Bot` already satisfies it, zero bot-side changes needed. `Broadcast` already resolves `"network:#chan"` entries across multiple networks in one call (`irc/network.go:754-772`).
  `Fetch()` per repo: read stored ETag, decrypt token, call the client; on error record status and `continue` (no seen/etag mutation, clean retry next cycle); on 304 record OK and `continue` (no parsing); otherwise iterate events **oldest-first** (GitHub returns newest-first — reverse like `rss/fetcher.go:263`), skip event types not in the repo's filter, skip already-seen event IDs, **mark seen before broadcasting** (same anti-retry-storm ordering as `rss/fetcher.go:275-279`), broadcast with a short pacing sleep between announcements. If `X-RateLimit-Remaining <= 1`, stop processing further repos this cycle (the poll interval itself is the backoff — no separate retry timer, matching the house style of `weather`/`omdb`). After the loop, prune `seen_events` rows older than 90 days (GitHub's own event-visibility window).

- **`github/client.go`** — hand-rolled HTTP client in the house style of `weather/`'s fetcher (explicit `context`, dedicated `*http.Client{Timeout: 15*time.Second}`, JSON struct decode, no retries). `FetchRepoEvents(ctx, owner, repo, token, etag) (*FetchResult, error)`: `GET https://api.github.com/repos/{owner}/{repo}/events`, headers `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2022-11-28`, `User-Agent`, `If-None-Match` when an ETag is stored, `Authorization: Bearer <token>` when a token is present. Handle 304 (short-circuit, no body read), 200 (decode), 403/429 (rate-limit error), other non-2xx (truncated-body error). Always parse `X-RateLimit-Remaining`/`X-RateLimit-Reset` off response headers.

  **Private-repo access**: GitHub has no username/password API auth (deprecated years ago). A repo's optional token is a **Personal Access Token** — either classic (needs `repo` scope) or fine-grained (scoped to just that repo, "Contents: read-only" + "Metadata: read-only" permissions). Both authenticate identically via the `Bearer` header above, so the client needs no special-casing between them. The dashboard's token field help text will recommend a fine-grained, read-only, single-repo token to minimize blast radius if it ever leaks. A public repo simply omits the token (unauthenticated request, lower rate limit).

- **`github/events.go`** — `ExtractAnnouncement(ev RawEvent, meta RepoMeta) (Announcement, bool)` dispatches on `ev.Type`:
  - `PushEvent`: pusher = `ev.Actor.Login`; branch from `payload.ref`; headline = most recent commit's first message line + "+N more" when `size>1`; link = compare link (`.../compare/{before}...{head}`) or single-commit link when `size==1`.
  - `PullRequestEvent`: surface `opened`/`reopened`/`closed` (check `pull_request.merged` to say "merged" instead of "closed"); skip `synchronize` (too noisy) by default.
  - `ReleaseEvent`: only `action=="published"` (skip draft-related actions).
  Each payload's exact JSON shape is documented in the design research and should be decoded via `json.Unmarshal(ev.Payload, &v)` per event type.

- **`github/format.go`** — mIRC bold/color templates, one style per event kind, mirroring `rss/irc_source.go`'s constant + assembler pattern:
  - Push (green tag): `[PUSH] **owner/repo** <pusher> pushed 3 commits to **main**: "fix: ..." (+2 more) 🔗 <link>`
  - PR (blue tag): `[PR] **owner/repo** <author> opened PR #42: "Fix bug" 🔗 <link>`
  - Release (purple tag): `[RELEASE] **owner/repo** <author> published v1.2.3 "Release 1.2.3" 🔗 <link>`
  (bold = `\x02`, color = `\x03FG,BG`, as used throughout `rss/irc_source.go`).
  Repo description is **not** inlined into every announcement (constant noise on a value that never changes). Instead it's fetched once when a repo is added via the dashboard, cached in `GitHubTrackerRepoConfig.CachedDescription`, and shown only in the web UI's repo list — keeping the poll loop to exactly one HTTP call per repo per cycle.

- **`github/db.go`** — new `data/github_seen.db` via `db.OpenDatabase` (mirrors `progtodo/db.go:39-75` for the `CREATE TABLE IF NOT EXISTS` + duplicate-column-tolerant `ALTER TABLE` migration pattern). Two tables: `seen_events (event_key TEXT PRIMARY KEY, repo, kind, occurred_at, added_at)` keyed by `"owner/repo#<github event id>"` (GitHub event IDs are already stable/unique/monotonic — no hash-dedup needed, simpler than `rss/dedup.go`), and `repo_etags (repo TEXT PRIMARY KEY, etag, updated_at)`. `MarkEventSeen` uses `INSERT OR IGNORE` to tolerate overlapping `Fetch()` calls without erroring.

- **`github/crypt.go`** — `Cryptor` wrapping AES-256-GCM. `NewCryptor(keyPath)` reads a 32-byte key from `data/github_secret.key`; if absent, generates one via `crypto/rand`, writes it with `0600`, logs `[SECURITY] Generated GitHub tracker PAT encryption key at ... — back this up` once (mirrors `web/auth_db.go:79-113`'s admin-password generation). `Encrypt`/`Decrypt` pass empty strings through unchanged (repos with no token stay blank); otherwise nonce||ciphertext, base64-encoded.

## `config/` additions

New `config/github_tracker.go`:
```go
type GitHubTrackerConfig struct {
    Enabled         bool                      `yaml:"enabled"`
    IntervalMinutes int                       `yaml:"interval_minutes"`
    Repos           []GitHubTrackerRepoConfig `yaml:"repos"`
}
type GitHubTrackerRepoConfig struct {
    Owner             string   `yaml:"owner"`
    Repo              string   `yaml:"repo"`
    Channels          []string `yaml:"channels"`              // "network:#chan"
    EventTypes        []string `yaml:"event_types,omitempty"` // subset of push/pull_request/release; empty = all
    TokenEncrypted    string   `yaml:"token_encrypted,omitempty" json:"-"`
    CachedDescription string   `yaml:"cached_description,omitempty"`
}
```
Add `GitHubTracker GitHubTrackerConfig `yaml:"github_tracker,omitempty"`` to `Config` (`config/config.go:29`, next to the existing unrelated `GitHub` field). Add `applyGitHubTrackerDefaults` (interval default when `<= 0`) called from `LoadConfig` alongside `applyRSSDefaults`. Extend `config/validate.go`'s `ValidateConfig`: reject duplicate `(Owner, Repo)` pairs (case-insensitive), require `IntervalMinutes > 0` when enabled, require each `EventTypes` entry to be one of `push`/`pull_request`/`release`, require non-empty `Owner`/`Repo` per entry.

## `web/` additions

New file `web/github_tracker.go` (own file, matching how `web/auth_db.go`/`web/rate_limiter.go` are split out):

- `handleGitHubTrackerSettings` — GET/POST enabled + interval, mirrors `handleRSSSettings` (`web/server.go:2075-2154`).
- `handleGitHubTrackerRepos` — GET list / POST add / DELETE by `?owner=&repo=`, mirrors `handleIRCNetworks` (`web/server.go:1817-1980`). Response DTO never includes the token or ciphertext, only `has_token bool` (same convention as IRC network rows never echoing `Services.Password`). POST does a best-effort one-off metadata GET to populate `CachedDescription`.
- `handleGitHubTrackerRepoEdit` — PUT/PATCH, mirrors `handleIRCNetworkEdit` (`web/server.go:1984-2073`): `owner`/`repo` immutable (rename = delete+add, same documented convention as IRC network names), `Channels`/`EventTypes` as pointers so omitted fields don't clobber, `Token *string` where nil/blank = keep existing ciphertext (exact mirror of the SASL-password blank-means-keep semantics at `web/server.go:2051-2059`).

All three guarded by the standard `isAdmin, _ := s.requireAdminCSRF(r)` check. Routes registered in `newServeMux()` alongside the RSS routes. `Server` struct gets one new field `githubFetcher *github.Fetcher`; `NewServer` gets one new param (encrypt/decrypt is reached via `s.githubFetcher.EncryptToken/DecryptToken`, so no separate cryptor param needed).

**Frontend** (`templates/index.html` + `templates/app.js`): new sidebar nav item + panel for settings (mirrors the RSS panel) and a repo-list-with-add-form section (mirrors the IRC-networks list). The channel picker in the add-repo form reuses the existing network `<select>`/`fetchIRCNetworks()` machinery already in `app.js` (~2469-2973) rather than duplicating it — checkboxes per configured channel across networks. Token field is `type="password"`, labeled "leave blank to keep existing," never pre-filled.

## Wiring

- **main.go**: construct `github.NewCryptor("data/github_secret.key")`, `github.NewDatabase("data/github_seen.db")` (deferred `Close()`), `github.NewFetcher(cfg, bot, ghDB, ghCryptor)`, `guard.Go("github tracker", githubFetcher.Start)` when enabled — same shape as the existing RSS block (`main.go:229-245`). Thread `githubFetcher` through the same positional-parameter path `rssFetcher` already takes: `rehashState` struct (`rehash_apply.go:20-30`), `startWebServer`/`runAsDaemon`/`runInForeground` signatures (`main.go:341-493`), into `web.NewServer(...)`.
- **rehash_apply.go**: add `githubFetcher *github.Fetcher` to `rehashState`; call `s.githubFetcher.ApplyConfig(newCfg)` in `doApplyRehash` right after the existing `s.rssFetcher.ApplyConfig(newCfg)` line (`rehash_apply.go:49`).
- **No new IRC command.** Web-UI-only CRUD is sufficient — repo/token/channel management isn't practical to type over IRC, unlike RSS's `!news on|off` toggle which controls something naturally flipped mid-conversation. If a status roundup is wanted later, extend `!stats` (`internal/ircusage/print.go:63`) with one added line, rather than a bespoke command.

## Test coverage

| File | Tests |
|---|---|
| `github/events_test.go` | Extract push/PR/release from fixture payloads; unknown type skipped; event-key stability |
| `github/client_test.go` | `httptest.NewServer`-based: sends `If-None-Match`; 304 short-circuits without decode; rate-limit headers parsed; `Bearer` header sent only when token set |
| `github/crypt_test.go` | Encrypt/decrypt round trip incl. empty-string passthrough; wrong key fails to decrypt; key file persists across `NewCryptor` calls (mode `0600`, same bytes reloaded) |
| `github/db_test.go` | Event-seen dedup round trip (double-mark doesn't error); ETag round trip |
| `github/fetcher_lifecycle_test.go` | Mirror `rss/fetcher_lifecycle_test.go` — stop-during-connect-wait |
| `config/github_tracker_test.go` | Defaults applied when interval is zero; YAML round-trip preserves all fields incl. opaque `TokenEncrypted`; duplicate owner/repo rejected by `ValidateConfig` |
| `config/network_channels_test.go` | Extend/move `rss_channels_test.go` after the §0 rename |
| `web/github_tracker_test.go` | Mirror `web/irc_network_edit_test.go`'s auth/CSRF test-setup pattern: add-repo persists ciphertext not plaintext; GET never returns a token field; blank-token PATCH preserves prior ciphertext; non-blank PATCH replaces it |

Acceptance bar (per CLAUDE.md): `go build .`, `go vet ./...`, `go test ./...`, `go test -race ./...` all clean.

## Sequencing

1. Config layer (`config/github_tracker.go` + validation + tests), optionally the `network_channels.go` rename first.
2. `github/` package core, each file with its own tests, compiling standalone with no `main.go` wiring yet: `crypt.go` → `db.go` → `client.go` → `events.go` → `format.go` → `fetcher.go`.
3. `main.go`/`rehash_apply.go` wiring; build + smoke-test with `github_tracker.enabled: false` (no-op path) before touching web.
4. `web/github_tracker.go` handlers + routes + `Server`/`NewServer` param + handler tests.
5. Frontend panel + JS; manually verify in the running dashboard: add a real public low-traffic repo, confirm settings persist across reload and `!rehash`/SIGHUP.
6. End-to-end smoke test against a real repo: confirm a push announces once with correct formatting, isn't re-announced next poll (dedup), and a quiet repo returns 304 (check via `RepoStatuses()`).

## Verification

- `go build .` and `go vet ./...` clean after each phase.
- `go test ./... ` and `go test -race ./...` clean, including all new test files above.
- Manual dashboard walkthrough: enable the tracker, add a public repo with a channel on one network and a channel on a second network, confirm both receive the announcement on the next push; add a private repo with a PAT, confirm `has_token: true` in the GET response and the token is never present in any API response or in `config.yaml` (only `token_encrypted` ciphertext); edit a repo leaving the token field blank, confirm the stored ciphertext is unchanged; send `!rehash` (or SIGHUP) after changing `interval_minutes` in the dashboard, confirm the poll ticker picks up the new interval without a bot restart.

### Critical files referenced throughout
- `rss/fetcher.go`, `rss/db.go`, `rss/irc_source.go`, `rss/dedup.go` — structural template
- `config/config.go`, `config/rss_channels.go`, `config/validate.go`
- `web/server.go` (handlers, routes, `Server`/`NewServer`), `web/auth_db.go` (key-generation precedent)
- `main.go`, `rehash_apply.go` — wiring
- `irc/network.go` (`Bot.Broadcast`), `db/sqlite.go` (`OpenDatabase`), `progtodo/db.go` (migration pattern)
