# `!gh` admin IRC commands + GitHub tracker upgrade

## Context

The GitHub repo activity tracker (`github/`) currently only has a web-dashboard UI for
managing tracked repos — there's no way for an IRC admin to add/remove/list/inspect
tracked repos from IRC itself, even though every other admin-manageable subsystem in this
bot (bookmarks, reminders, tickets, RSS toggles) has an IRC command surface. The user asked
for a `!gh` command family (`list`/`add`/`del`/`search`) and, while reviewing the tracker's
current behavior, decided to upgrade the tracker itself along the way:

- It only announces push/PR/release today; issues and branch/tag create/delete are cheap
  to add (same `/events` poll, just unparsed).
- Announcement messages don't surface the GitHub identifier (SHA/PR#/tag) in a scannable
  way — it's buried in body text or only in the link.
- There's no way to look up a specific commit/PR/issue or search recent activity from IRC.
- Long `github.com/.../compare/<sha>...<sha>` links eat message space that could show more
  useful text instead — and this bot already has a working URL shortener (`rss.ShortenURLWithService`)
  used for RSS links, that was never wired into the GitHub tracker.
- Letting an admin paste a GitHub PAT into IRC is a real credential-leak risk (server/bouncer/
  client logs) unless it's gated behind a verified encrypted connection and never done in a
  channel.

This plan covers all of the above as one coherent change to `github/` + a new `irc/` command
family, wired through the bot's existing disk-based config/rehash pipeline (the tracker's
`Fetcher` owns its own config snapshot, separate from `irc.Bot`'s — any mutation from IRC
*must* go through `config.SaveConfig` + `bot.RunRehash`, the same path the web dashboard uses,
or the fetcher will never see the change).

---

## 1. Config & validation (`config/github_tracker.go`, `config/validate.go`)

- `GitHubTrackerRepoConfig.EventTypes` doc comment gains three new valid filter strings:
  `"issues"`, `"create"` (branch or tag created), `"delete"` (branch or tag deleted). No new
  Go fields — `EventTypes` is already `[]string`.
- `validateGitHubTracker`'s `validEvents` map gains `"issues": true, "create": true, "delete": true`,
  and its error message lists them.
- No new fields needed on `GitHubTrackerConfig`/`GitHubTrackerRepoConfig` for anything else in
  this plan (search/tokens/URL-shortening all reuse existing config: `RSS.URLShortener`,
  `TokenEncrypted`).

## 2. `github/` package changes

### 2.1 New event types + a `RefID` on every announcement (`github/events.go`)

Add a `RefID string` field to `Announcement` (alongside existing `RepoFullName, Kind, Message`) —
the natural GitHub identifier for that event, so it can go **in the `[KIND ...]` tag** instead
of buried in body text:

| Kind | RefID | Example tag |
|---|---|---|
| push | short SHA of head commit (7 chars) | `[PUSH 06dfee0]` |
| pull_request | `#<number>` | `[PR #123]` |
| release | tag name | `[RELEASE v0.4.2]` |
| issues (new) | `#<number>` | `[ISSUE #45]` |
| create (new) | ref name (branch or tag) | `[BRANCH main]` / `[TAG v1.0]` |
| delete (new) | ref name | `[BRANCH old-feature]` (no link — ref no longer exists) |

`ExtractAnnouncement`'s switch gains `case "IssuesEvent"`, `case "CreateEvent"`, `case "DeleteEvent"`
dispatching to new `extractIssue`/`extractCreate`/`extractDelete`, mirroring the existing
`extractPush`/`extractPullRequest`/`extractRelease` shape (own payload struct, `json.Unmarshal(ev.Payload, &p)`,
early `return Announcement{}, false` for anything not in scope):

- **`extractIssue`**: only `action ∈ {"opened", "closed"}` announce (matches product decision —
  no reopened/labeled/assigned/etc.). Author falls back to `ev.Actor.Login` if the trimmed
  payload omits `issue.user.login` (same trimmed-payload defensiveness as `extractPullRequest`
  already has). Link falls back to a constructed `.../issues/{number}` URL if `html_url` is
  missing.
- **`extractCreate`** / **`extractDelete`**: payload is `{RefType string "branch"|"tag", Ref string}`.
  Ignore `ref_type == "repository"` (CreateEvent also fires for repo creation itself — not
  relevant here). `extractCreate` builds a `.../tree/{ref}` link; `extractDelete` has **no link
  at all** (the ref no longer exists) — its formatter must not call the link-suffix helper.
  Both stay single `Kind` values (`"create"`/`"delete"`, not split by branch/tag) — the
  branch-vs-tag word only changes the tag's label text.
- `kindLabel` (used by `CollapseForBroadcast`'s "(+N more X)" suffix) gains `"issues"`,
  `"refs created"`, `"refs deleted"`.
- `extractPullRequest` drops the redundant `"PR #N"` from its body text (now in the tag) and
  sets `RefID`; `extractRelease` sets `RefID = tagName` and only mentions the release's `name`
  in the body when it differs from the tag (avoid saying the same thing twice).

### 2.2 URL shortening — reuse `rss.ShortenURLWithService`, don't rebuild it

`rss/fetcher.go` already exports a generic, already-proven-in-production shortener
(`ShortenURLWithService(longURL, preferredService string) string`, backed by is.gd/tinyurl/v.gd/
clck.ru/da.gd with automatic fallback across services and a graceful "return the original URL"
result if every service fails). `github/` should call this directly rather than adding a second
shortener implementation or a new config knob — reuse the existing `RSS.URLShortener` preference
as the bot-wide "preferred shortener" setting.

Wire it in `RepoMeta` (already threaded into every `extract*` call): add
`URLShortener string` to `RepoMeta`, populated once per fetch cycle in `fetcher.go` from
`f.cfg.RSS.URLShortener`. Each `extract*` function calls
`link = rss.ShortenURLWithService(link, meta.URLShortener)` right before building its `Announcement`
— i.e. shorten once, use the short link both in the formatted `Message` and (for search) in
whatever gets persisted. `extractDelete` has no link, so nothing to shorten there. Also apply the
same call to the `HTMLURL` returned by the new live-lookup functions (§2.4) before formatting a
`!gh search` reply, for consistency — same helper, same config setting.

### 2.3 `github/format.go` — tag now carries the `RefID`

Replace the per-kind static tag constants with one small helper that builds the bracket text
from a color code, label, and `refID` (empty `refID` renders as `[LABEL]` with no trailing
identifier, used nowhere today but keeps the helper general):

```go
func ircTag(colorCode, label, refID string) string {
    if refID == "" {
        return "\x03" + colorCode + ",01[" + label + "]\x03"
    }
    return "\x03" + colorCode + ",01[" + label + " " + refID + "]\x03"
}
```

Update every `format*` signature to accept `refID`/drop now-redundant `number` params, and add
`formatIssue`, `formatCreate` (label = `strings.ToUpper(refType)`, i.e. `BRANCH`/`TAG`), and
`formatDelete` (same tag shape, **no link parameter at all** — delete events never call the
🔗-suffix helper). Update all call sites in `events.go` accordingly.

### 2.4 `github/client.go` — three new one-shot lookup calls, for `!gh search`

Mirror `FetchRepoEvents`'s conventions (same `eventsHTTPClient`, `apiBase`, `X-GitHub-Api-Version`
header, Bearer-if-token) but simplified — no ETag, no rate-limit struct, just `(result, nil)` on
200, `(nil, nil)` on 404 (an expected, routine outcome for the search dispatch logic below, not
an error), `(nil, err)` on anything else:

```go
func FetchPullRequest(ctx context.Context, owner, repo, token string, number int) (*PullRequestInfo, error) // GET /pulls/{number}
func FetchIssue(ctx context.Context, owner, repo, token string, number int) (*IssueInfo, error)             // GET /issues/{number}
func FetchCommit(ctx context.Context, owner, repo, token, sha string) (*CommitInfo, error)                  // GET /commits/{sha}
```

Factor the shared "build an authenticated GET request with the standard headers" step out of
`FetchRepoEvents` into a small private helper so it's not duplicated four times.

**Note for CLAUDE.md**: these are on-demand calls triggered by `!gh search`, not part of the
polling cycle — they don't share the `Fetch()` loop's rate-limit backoff (`fetcher.go`'s
"pause remaining repos if `RateRemaining <= 1`" logic only covers the `/events` poll). A burst
of `!gh search` calls has no rate-limit protection of its own; documented as a known limitation,
not fixed by this plan.

### 2.5 `github/db.go` — persist enough to search locally

`seen_events` gains two columns via this repo's existing tolerant-migration convention
(`ALTER TABLE ... ADD COLUMN`, errors logged/ignored):

```sql
ALTER TABLE seen_events ADD COLUMN ref_id TEXT;
ALTER TABLE seen_events ADD COLUMN message TEXT;
```

`MarkEventSeen` gains `refID, message string` params and stores them. New:

```go
// SearchEvents returns up to limit rows for repo (newest first) whose message or ref_id
// matches pattern. Matching happens in Go (regexp.MatchString per row after a plain
// "WHERE repo = ?" SQL fetch), not via a SQL REGEXP extension — seen_events is small and
// single-repo-filtered (~90 day retention), so a full scan-and-filter in Go is simpler and
// dependency-free.
func (d *Database) SearchEvents(repo string, pattern *regexp.Regexp, limit int) ([]SeenEvent, error)
```

The IRC layer compiles the admin's query as `regexp.Compile("(?i)" + query)`; if that fails
(invalid regex syntax), fall back to `regexp.Compile("(?i)" + regexp.QuoteMeta(query))` so a
literal search always works even for a typo'd regex — keep this fallback decision in the IRC
handler, leave `SearchEvents` itself a pure DB read given an already-compiled pattern.

### 2.6 `github/fetcher.go` — thread `RefID`/pre-collapse `Message` through, add new filters

`announceNewEvents` passes `ann.RefID, ann.Message` into the new `MarkEventSeen` signature —
using the **pre-collapse**, per-event message (computed before `CollapseForBroadcast` runs), so
search sees one row per real event even though broadcast collapses bursts into one line.
`allowedEventTypeSet` gains `"issues" → "IssuesEvent"`, `"create" → "CreateEvent"`,
`"delete" → "DeleteEvent"`.

---

## 3. `irc/` package — the `!gh` command family

### 3.1 Wiring

`irc.Bot` doesn't hold a `*github.Fetcher` today (only `web.Server` does). Add it the same way
`SetRSSDatabase`/`SetBookmarksDatabase` were added: a `githubFetcher *github.Fetcher` field +
`SetGitHubFetcher(f *github.Fetcher)` setter, wired in `main.go` right after
`github.NewFetcher(...)` is constructed. No separate cryptor plumbing needed — `github.Fetcher`
already exposes `EncryptToken`/`DecryptToken`, which `irc/` calls the same way `web/` does.

### 3.2 Command surface

Admin-gated (own `HasPrefix` + own `isAdmin && isLoggedInAdmin` check, same shape as `!ticket` —
not nested in the shared admin block), dispatched from a new `irc/github_admin.go`:

```
!gh list
!gh add <owner>/<repo> [network:#chan ...] [--private]
!gh del <owner>/<repo>
!gh search <owner>/<repo> <query>
```

- **`list`**: one line per tracked repo (manual loop + `time.Sleep(500ms)` between lines, same
  convention as `!ticket pending`/`!news` — not `sendPrivmsgMentionedLines`, which is for a single
  wrapped reply, not itemized rows), showing channels, event-type filter, and whether a token is
  set (never the token itself).
- **`add`** (public/no-token path): parse `owner/repo` + channel args, dup-check via
  `config.FindGitHubTrackerRepo`, clone config (`config.CloneConfig`), append the new repo entry,
  `config.ValidateConfig` the clone, `config.SaveConfig`, `b.RunRehash(...)`, then best-effort
  `JoinChannelSession` per channel (mirrors `web/github_tracker.go`'s `joinAnnounceChannels` —
  worth factoring that into a shared helper both `web/` and `irc/` call, as a nice-to-have, not
  required for correctness).
- **`add --private`** (token path) — see §3.3, the security-sensitive part of this feature.
- **`del`**: dup-check, filter out, validate/save/rehash. Does not part any channels (matches
  the web dashboard's own DELETE behavior — leaving channel cleanup manual is consistent, not a
  regression).
- **`search`**: see §3.4.

### 3.3 `!gh add --private` — fail-closed, PM-only token flow

This is the part the user was explicit about: **never** accept a raw PAT typed in a channel, and
only offer the PM flow when the admin's own connection can be verified encrypted.

1. If invoked from a channel (`strings.HasPrefix(target, "#")`), refuse immediately: *"Private-repo
   tokens can only be added in a PM to me — resend this in a query."* Never proceeds to any
   check below from a channel.
2. If invoked in a PM: issue a `WHOIS <sender>` and wait (bounded, 5s timeout) for numeric `671`
   (`RPL_WHOISSECURE`, "is using a secure connection") tagged to that nick, using
   `ircevent.Connection`'s existing `Send`/`AddCallback` primitives — this repo already registers
   numeric callbacks the same way for SASL (`irc/network.go:343-354`), so this is a direct
   extension of an existing pattern, not new library capability. **Fail closed**: if `671`
   doesn't arrive before the timeout or before `RPL_ENDOFWHOIS` (318), treat the connection as
   "not verifiably secure" — even if it's actually TLS, since not every ircd sends 671 (Libera.Chat
   and OFTC do; smaller/older ircds may not). Reply: *"Can't verify this connection is encrypted.
   Add private-repo tokens via the web dashboard instead."* and stop — this is a known, accepted
   limitation on less mainstream networks, not a bug.
3. If verifiably secure: register a short-lived pending-token entry (in-memory map keyed like the
   existing admin-session-key pattern, network+nick, 120s expiry timer) and reply *"Reply in this
   PM with the PAT for `<owner>/<repo>` within 120s. It will not be echoed or logged."*
4. On the next PRIVMSG from that nick in that same PM (checked **before** the normal `!`-prefix
   command dispatch, and before the channel-logger call that currently logs every PRIVMSG
   unconditionally — this one message must be excluded from `logs/` to avoid writing a plaintext
   secret to disk): treat it as the token, immediately encrypt via `githubFetcher.EncryptToken`,
   discard the plaintext, and finish the add exactly like the public path. Never log or echo the
   raw token anywhere.

### 3.4 `!gh search <owner>/<repo> <query>` — smart dispatch

Repo must already be tracked (reuses its stored token, if any, and its dedup/history cache) —
decrypt via `githubFetcher.DecryptToken`, then dispatch on `query`:

- **Numeric** (`^#?\d+$`): try `github.FetchPullRequest`; on a 404, fall back to
  `github.FetchIssue` (PRs and issues share GitHub's numbering space). Reply with a one-line
  `[PR #N]`/`[ISSUE #N]`-tagged summary (shortened link per §2.2), or "no PR or issue #N found".
- **Hex string, 7–40 chars** (`^[0-9a-f]{7,40}$`, case-insensitive): `github.FetchCommit`. Reply
  `[COMMIT <short>] message by author 🔗 link`, or "no commit found".
- **Anything else**: local text/regex search via `Database.SearchEvents` (§2.5) — reply up to 5
  matches (same loop+sleep convention as `list`), or "no matches".

Each live-lookup branch runs with a bounded `context.WithTimeout` (15s, matching the existing
`eventsHTTPClient` timeout convention) — commands already run on their own per-message goroutine
(`irc/network.go`'s `cmdSem`-bounded dispatch), so a slow GitHub call here doesn't block the IRC
read loop.

---

## 4. Docs & usage text

- `internal/ircusage/print.go`: add four `adminLines` rows for `!gh list/add/del/search`
  (template: the existing `!ticket ...` row shape).
- `irc/bot.go`'s own `!help` admin summary string (built independently of `ircusage`): append a
  terse `!gh list/add/del/search` mention — don't spell out `--private` inline, point to full
  usage instead.
- `CLAUDE.md` "GitHub Tracker" section: note the new `issues`/`create`/`delete` event filters;
  add a new fine-grained PAT scope bullet — **Issues** (read), needed for private repos once
  `IssuesEvent`/issue-lookup is in use; document that `!gh search`'s live lookups are on-demand
  calls outside the polling/rate-limit-backoff loop; document the PM-only, WHOIS-gated token
  flow as the only way to set a token from IRC (editing an existing repo's token stays
  dashboard-only).

---

## 5. Testing

- `github/events_test.go`: new cases for issue/create/delete extraction (action filtering,
  ref_type filtering, no-link-on-delete), updated assertions for `RefID` and the tag text move
  (body text no longer contains `"PR #12"`), `CollapseForBroadcast` case mixing the new kinds.
- New `github/format_test.go` (doesn't exist yet): one case per `format*` asserting exact tag
  text (`[PUSH 06dfee0]`, `[DELETE ...]` has no 🔗 byte sequence, etc).
- `github/db_test.go`: round-trip test for the new `ref_id`/`message` columns, new
  `SearchEvents` tests (match by message, match by ref_id, repo filtering, limit, case
  insensitivity), and a migration-safety test opening a DB seeded with the old schema.
- `github/client_test.go`: new `FetchPullRequest`/`FetchIssue`/`FetchCommit` tests (200, 404 →
  `nil, nil`, Bearer-auth-when-token-set), reusing the existing `withTestServer` helper.
- `github/fetcher_announce_test.go`: extend for the three new `EventTypes` filter strings; add a
  test confirming `MarkEventSeen` gets the pre-collapse per-event message, not the collapsed one.
- `config/github_tracker_test.go`: extend `validateGitHubTracker` coverage for the new filter
  strings.
- `irc/`: no existing test stands up a fake IRC connection for `handleCommand`, so keep new tests
  narrowly scoped to pure-function pieces (owner/repo parsing, the numeric/hex/text query-dispatch
  regexes as standalone functions). `checkWhoisSecure` isn't practically unit-testable without a
  fake `ircevent.Connection` — flag as an accepted gap, verify manually against a real ircd
  (Libera.Chat/OFTC both send 671) instead.

## Verification

1. `go build .` / `go vet ./...` / `go test ./...` (and `go test -race ./...` given the new
   in-memory pending-token map + timer).
2. Manually run the bot against a real network (or a local test ircd) with `!gh add owner/repo`
   for a public repo, confirm it shows up via `!gh list`, generates announcements with the new
   `[KIND id]` tags and shortened links on the next poll, and `!gh search owner/repo <PR#>` /
   `<sha>` / `<text>` all return sensible replies.
3. Manually verify the `--private` flow's PM-only + WHOIS gating on a real network known to send
   671 (e.g. Libera.Chat) — confirm a channel-invoked attempt is refused, a PM attempt over a
   plaintext (non-TLS) connection is refused with the "use the dashboard" message, and a PM
   attempt over TLS successfully prompts for and accepts a token without it appearing in
   `logs/`.

### Critical files
`github/events.go`, `github/format.go`, `github/client.go`, `github/db.go`, `github/fetcher.go`,
`irc/bot.go`, `irc/network.go` (new `irc/github_admin.go`), `internal/ircusage/print.go`,
`config/github_tracker.go`, `config/validate.go`, `CLAUDE.md`.
