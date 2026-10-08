# `!gh` admin IRC commands + GitHub tracker extension — implementation plan

Scope: extend `github/` (new event types, richer `[KIND id]` tags, live lookup calls,
local search) and add a new `!gh` admin command family in `irc/`, wired through the
existing config/rehash pipeline. No code written yet — this is the design doc.

---

## 0. Verified facts that gate the design (read code, not guessed)

- **ergochat/irc-go v0.6.0 (`ircevent.Connection`) has no `Whois()` helper**, but it
  exposes a fully generic `func (irc *Connection) Send(command string, params ...string) error`
  (irc.go:387) — so `n.conn.Send("WHOIS", nick)` is a normal, supported call.
  Numeric replies are dispatched through `AddCallback(code string, cb func(ircmsg.Message))`
  (irc_callback.go:30), exactly the mechanism already used in this repo for SASL numerics
  900/903/902/904/905 (`irc/network.go:343-354`). **RPL_WHOISSECURE (numeric "671") can be
  registered the same way**: `n.conn.AddCallback("671", func(e ircmsg.Message) {...})`.
  For that numeric, `e.Params` is `[ourNick, targetNick, "is using a secure connection"]` per
  the IRC WHOIS numeric convention — correlate on `e.Params[1]` case-folded.
  There is no dedicated "whois done" numeric callback wired in this repo today (317/318/
  319/320/318 RPL_WHOISIDLE/RPL_ENDOFWHOIS etc. are all unregistered) — the plan below adds
  a callback for "318" (RPL_ENDOFWHOIS) too, to know when to give up early instead of always
  waiting the full timeout.
- **PM vs channel-message detection**: this repo has no dedicated helper; the existing,
  already-established convention (`irc/social.go:76`, `irc/bot.go:665` op/voice block) is
  `strings.HasPrefix(target, "#")` for "is a channel". Reuse this exact test for consistency
  (`isPM := !strings.HasPrefix(target, "#")`) rather than inventing a nick-comparison method.
- **`irc.Bot` does not currently hold a reference to `github.Fetcher`/`github.Cryptor` at
  all** — only `web.Server` does (`web/server.go:59`, wired in `main.go:352-364`). This must
  be added via a `SetGitHubFetcher` method, following the exact existing pattern of
  `SetRSSDatabase`/`SetBookmarksDatabase`/`SetUploadsDatabase` (`irc/bot.go:141-178`,
  wired in `main.go` right after `bot.SetCryptoDatabase(cryptoDB)`, since `githubFetcher`
  is constructed a few lines later at `main.go:343-354` — the `bot.SetGitHubFetcher(githubFetcher)`
  call must go *after* that construction, so around `main.go:355`, before `rstate` is built).
- **Config mutation from IRC must go through the same disk-based rehash path the web
  dashboard uses**: clone `b.GetConfig()` (the bot's own atomic pointer, via
  `config.CloneConfig`, `config/rehash_diff.go:13`), mutate the clone, `config.ValidateConfig`
  the full clone (not a scratch struct — unlike `web/github_tracker.go`'s POST handler, IRC
  has no separately-tracked `s.cfg`, so validate the real full clone to also catch unrelated
  drift), `config.SaveConfig(config.DefaultConfigPath, clone)`, then `b.RunRehash(source)`
  (`irc/bot.go:190`) — this reloads from disk and calls `githubFetcher.ApplyConfig(newCfg)`
  itself (`rehash_apply.go:49`). Never call `githubFetcher.SetConfig`/`ApplyConfig` directly
  from the IRC handler.

---

## 1. Config/schema changes

### `config/github_tracker.go`
- Extend the doc comment on `GitHubTrackerRepoConfig.EventTypes` to list the two new
  filter strings: `"issues"`, `"create"` (covers both branch+tag creation), `"delete"`
  (covers both branch+tag deletion). No new Go fields needed on this struct — `EventTypes`
  is already `[]string`.
- No new fields required for `GitHubTrackerConfig` itself.

### `config/validate.go`
- In `validateGitHubTracker`, extend the `validEvents` map:
  ```go
  validEvents := map[string]bool{
      "push": true, "pull_request": true, "release": true,
      "issues": true, "create": true, "delete": true,
  }
  ```
  and update the error message's parenthetical list of valid values.

### `config/github_tracker_test.go`
- Add/extend a table-driven case asserting the new filter strings pass `validateGitHubTracker`
  and that an invalid one (e.g. `"watch"`) is still rejected.

---

## 2. `github/` package changes

### 2.1 `github/events.go` — new event types + `RefID`

**`Announcement` struct** (currently `RepoFullName, Kind, Message`) gets one new field:
```go
type Announcement struct {
    RepoFullName string
    Kind         string // "push" | "pull_request" | "release" | "issues" | "create" | "delete"
    RefID        string // the natural identifier for the [KIND ...] tag: short SHA, "#N", tag/branch name
    Message      string
}
```
`RefID` is populated by each `extract*` function and threaded into `format*` so the tag
itself carries the identifier (see 2.2). `Message` keeps holding the body text *after* the
tag (format.go still prepends the tag using `Kind`+`RefID`).

**`ExtractAnnouncement` switch** gains three cases:
```go
switch ev.Type {
case "PushEvent":
    return extractPush(ev, meta)
case "PullRequestEvent":
    return extractPullRequest(ev, meta)
case "ReleaseEvent":
    return extractRelease(ev, meta)
case "IssuesEvent":
    return extractIssue(ev, meta)
case "CreateEvent":
    return extractCreate(ev, meta)
case "DeleteEvent":
    return extractDelete(ev, meta)
default:
    return Announcement{}, false
}
```

**`extractPush`** changes: compute `shortSHA := p.Head[:min(7, len(p.Head))]` and set
`Announcement.RefID = shortSHA`; pass it to `formatPush` instead of embedding only in the
link. (Existing `headline`/`more`/`link` logic unchanged.)

**`extractPullRequest`** changes: set `RefID = "#" + strconv.Itoa(p.Number)`; drop the
`"PR #" + strconv.Itoa(number)` substring from the body text passed to `formatPullRequest`
since the tag now carries it (title stays quoted in the body).

**`extractRelease`** changes: set `RefID = p.Release.TagName`; keep `name` (release title,
if different from tag) in the body text only when `name != tag`.

**New `issuesPayload` + `extractIssue`**:
```go
type issuesPayload struct {
    Action string `json:"action"`
    Issue  struct {
        Number  int    `json:"number"`
        Title   string `json:"title"`
        HTMLURL string `json:"html_url"`
        User    struct{ Login string `json:"login"` } `json:"user"`
    } `json:"issue"`
}

func extractIssue(ev RawEvent, meta RepoMeta) (Announcement, bool) {
    var p issuesPayload
    if err := json.Unmarshal(ev.Payload, &p); err != nil {
        return Announcement{}, false
    }
    switch p.Action {
    case "opened", "closed":
    default:
        return Announcement{}, false
    }
    author := ev.Actor.Login
    if p.Issue.User.Login != "" {
        author = p.Issue.User.Login
    }
    link := p.Issue.HTMLURL
    if link == "" {
        link = "https://github.com/" + ev.Repo.Name + "/issues/" + strconv.Itoa(p.Issue.Number)
    }
    return Announcement{
        RepoFullName: ev.Repo.Name,
        Kind:         "issues",
        RefID:        "#" + strconv.Itoa(p.Issue.Number),
        Message:      formatIssue(ev.Repo.Name, author, p.Action, p.Issue.Title, link),
    }, true
}
```
(Per product decision: only `opened`/`closed` actions announce — no reopened/labeled/
assigned/etc.)

**New `createDeletePayload` + `extractCreate`/`extractDelete`**:
```go
type createDeletePayload struct {
    RefType string `json:"ref_type"` // "branch" | "tag"
    Ref     string `json:"ref"`
}

func extractCreate(ev RawEvent, meta RepoMeta) (Announcement, bool) {
    var p createDeletePayload
    if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Ref == "" {
        return Announcement{}, false
    }
    if p.RefType != "branch" && p.RefType != "tag" {
        return Announcement{}, false // ignore CreateEvent for repo-creation itself (ref_type "repository")
    }
    link := "https://github.com/" + ev.Repo.Name + "/tree/" + p.Ref
    return Announcement{
        RepoFullName: ev.Repo.Name,
        Kind:         "create",
        RefID:        p.Ref,
        Message:      formatCreate(ev.Repo.Name, ev.Actor.Login, p.RefType, p.Ref, link),
    }, true
}

func extractDelete(ev RawEvent, meta RepoMeta) (Announcement, bool) {
    var p createDeletePayload
    if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Ref == "" {
        return Announcement{}, false
    }
    if p.RefType != "branch" && p.RefType != "tag" {
        return Announcement{}, false
    }
    // no link: the ref no longer exists, per product decision.
    return Announcement{
        RepoFullName: ev.Repo.Name,
        Kind:         "delete",
        RefID:        p.Ref,
        Message:      formatDelete(ev.Repo.Name, ev.Actor.Login, p.RefType, p.Ref),
    }, true
}
```
`Kind` for create/delete stays a single value each (`"create"`/`"delete"`) rather than
splitting branch vs tag into four kinds — `RefType` only affects the tag word (`BRANCH`
vs `TAG`) inside `format.go`, and `CollapseForBroadcast` still collapses per `Kind` as
today (a burst of 3 branch creates + 1 tag create in one poll cycle collapses to one
`[BRANCH ...]`-tagged line + "(+3 more branches/tags)" — see `kindLabel` below).

**`kindLabel`** gains:
```go
case "issues":
    return "issues"
case "create":
    return "refs created"
case "delete":
    return "refs deleted"
```

### 2.2 `github/format.go` — new tag shapes carrying `RefID`

New IRC color tags:
```go
ircIssueTag  = "\x0308,01[ISSUE]\x03"  // yellow on black
ircCreateTag = "\x0310,01[BRANCH]\x03" // teal on black — actual word (BRANCH/TAG) built dynamically, see below
ircDeleteTag = "\x0304,01[DELETE]\x03" // red on black — kind word (BRANCH/TAG) appended dynamically
```
Since the tag must show `[PUSH 06dfee0]`, `[PR #12]`, `[RELEASE v0.4.2]`, `[ISSUE #7]`,
`[BRANCH foo]`, `[TAG v0.5.0]` — i.e. the *bracket text itself* varies by both kind and
by branch-vs-tag for create/delete — the cleanest implementation is to stop hardcoding
static tag constants per kind and instead build the bracketed tag from (color, label,
refID) with one shared helper:
```go
func ircTag(colorCode, label, refID string) string {
    if refID == "" {
        return "\x03" + colorCode + ",01[" + label + "]\x03"
    }
    return "\x03" + colorCode + ",01[" + label + " " + refID + "]\x03"
}
```
Then:
- `formatPush(repoFullName, pusher, branch string, size int, headline, more, link, shortSHA string) string`
  — prepend `ircTag("09", "PUSH", shortSHA)` instead of `ircPushTag`.
- `formatPullRequest(repoFullName, author, action string, title, link string) string`
  (drops the `number int` param — no longer needed in the body) — tag is
  `ircTag("12", "PR", "#"+strconv.Itoa(number))`, built by the caller (events.go) and
  passed in as `refID string` param instead, to keep format.go free of number-formatting
  duplication: i.e. actual signature is
  `formatPullRequest(repoFullName, author, action, refID, title, link string) string`.
- `formatRelease(repoFullName, author, tag, name, link string) string` — tag bracket uses
  `ircTag("06", "RELEASE", tag)`; body only mentions `name` when `name != tag` (else omit
  the redundant quoted name).
- `formatIssue(repoFullName, author, action, refID, title, link string) string` — new;
  `ircTag("08", "ISSUE", refID)` + `repoFullName author <opened|closed> issue: "title" 🔗 link`.
- `formatCreate(repoFullName, author, refType, refID, link string) string` — new; tag label
  is `strings.ToUpper(refType)` (i.e. "BRANCH" or "TAG"): `ircTag("10", strings.ToUpper(refType), refID)`
  + body `repoFullName author created <refType> 🔗 link` (refID already in the tag, so body
  doesn't repeat the name).
- `formatDelete(repoFullName, author, refType, refID string) string` — new; same tag
  pattern, no link param at all (delete events never link, per product decision) — body:
  `repoFullName author deleted <refType>` (no 🔗 suffix call at all).

All existing call sites in `events.go` update their `format*` calls to match the new
signatures (drop `number`, add `refID`/`shortSHA` params as above).

### 2.3 `github/client.go` — three new live lookup methods

Mirror `FetchRepoEvents`'s conventions exactly (same `eventsHTTPClient`, `apiBase`,
`userAgent`, `X-GitHub-Api-Version` header, Bearer-if-token, 15s timeout — no ETag/
If-None-Match on these since they're one-shot, not polled).

```go
// PullRequestInfo is the subset of a single PR lookup used for an IRC reply.
type PullRequestInfo struct {
    Number  int
    Title   string
    State   string // "open" | "closed"
    Merged  bool
    Author  string
    HTMLURL string
}

// FetchPullRequest calls GET /repos/{owner}/{repo}/pulls/{number}. Returns
// (nil, nil) on 404 (caller falls back to FetchIssue) rather than an error, since a 404
// here is an expected/routine outcome of the smart-dispatch logic, not a failure.
func FetchPullRequest(ctx context.Context, owner, repo, token string, number int) (*PullRequestInfo, error)

// IssueInfo is the subset of a single issue lookup used for an IRC reply.
type IssueInfo struct {
    Number  int
    Title   string
    State   string // "open" | "closed"
    Author  string
    HTMLURL string
}

// FetchIssue calls GET /repos/{owner}/{repo}/issues/{number}. Returns (nil, nil) on 404.
func FetchIssue(ctx context.Context, owner, repo, token string, number int) (*IssueInfo, error)

// CommitInfo is the subset of a single commit lookup used for an IRC reply.
type CommitInfo struct {
    SHA        string
    ShortSHA   string
    Message    string // first line only
    Author     string
    HTMLURL    string
}

// FetchCommit calls GET /repos/{owner}/{repo}/commits/{sha}. Returns (nil, nil) on 404.
func FetchCommit(ctx context.Context, owner, repo, token, sha string) (*CommitInfo, error)
```
Each follows the same `http.NewRequestWithContext` → set headers → `eventsHTTPClient.Do`
→ status-code switch pattern as `FetchRepoEvents`, but simplified (no ETag, no rate-limit
struct — just return `(nil, nil)` on 404, `(nil, err)` on any other non-200, decode+return
on 200). Factor the common "build authenticated GET request" step into a small private
helper (`newAPIRequest(ctx, method, url, token string) (*http.Request, error)`) shared by
all four functions (including the existing `FetchRepoEvents`) to avoid duplicating the
four `req.Header.Set(...)` lines three more times — this is a refactor of `client.go`,
not just an addition.

### 2.4 `github/db.go` — new columns + `SearchEvents`

`seen_events` schema gains two columns, added via the existing tolerant-migration
convention (`_, _ = sqldb.Exec(...)`, matching `rss/db.go:51-53`):
```go
_, _ = sqldb.Exec(`ALTER TABLE seen_events ADD COLUMN ref_id TEXT`)
_, _ = sqldb.Exec(`ALTER TABLE seen_events ADD COLUMN message TEXT`)
_, _ = sqldb.Exec(`UPDATE seen_events SET ref_id = '' WHERE ref_id IS NULL`)
_, _ = sqldb.Exec(`UPDATE seen_events SET message = '' WHERE message IS NULL`)
```
These two placed right after the existing `CREATE TABLE IF NOT EXISTS seen_events` block
in `NewDatabase`.

**`MarkEventSeen` signature change** (breaking — update the one call site):
```go
func (d *Database) MarkEventSeen(eventKey, repo, kind, refID, message string, occurredAt time.Time) error {
    _, err := d.db.Exec(
        `INSERT OR IGNORE INTO seen_events (event_key, repo, kind, ref_id, message, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`,
        eventKey, repo, kind, refID, message, occurredAt,
    )
    return err
}
```

**New `SearchEvents`**:
```go
// SearchEvents returns up to limit seen_events rows for repo (newest first) whose
// message or ref_id matches pattern (a Go regexp, case-insensitive — caller should
// wrap with "(?i)" or use regexp.MustCompile with the CI flag as needed). Matching is
// done in Go, not SQL: seen_events is bounded (~90 day retention, single-repo filtered),
// so a full table scan into memory plus regexp.MatchString per row is cheap and avoids
// depending on a SQLite REGEXP extension. Returns rows in occurred_at DESC order.
func (d *Database) SearchEvents(repo string, pattern *regexp.Regexp, limit int) ([]SeenEvent, error)

// SeenEvent is one row read back from seen_events (for !gh search's local-text-search path).
type SeenEvent struct {
    EventKey   string
    Repo       string
    Kind       string
    RefID      string
    Message    string
    OccurredAt time.Time
}
```
Implementation: `SELECT event_key, repo, kind, ref_id, message, occurred_at FROM seen_events
WHERE repo = ? ORDER BY occurred_at DESC` (no SQL-side pattern filter), then in Go: iterate
rows, `if pattern.MatchString(row.Message) || pattern.MatchString(row.RefID) { append; if
len(out) >= limit { break } }`. Compile the caller-supplied text as `regexp.Compile("(?i)" +
userText)`; on `regexp.Compile` error (bad pattern), the IRC layer should fall back to
`regexp.QuoteMeta(userText)` wrapped the same way, so a literal search always at least works
even if the admin typed invalid regex syntax — decide and document this fallback explicitly
in the IRC handler (§3.4), not silently in `SearchEvents` itself (keep `SearchEvents` a pure
DB read given an already-compiled `*regexp.Regexp`).

### 2.5 `github/fetcher.go` — thread `RefID`/`Message` through to `MarkEventSeen`

In `announceNewEvents`, change:
```go
if err := f.db.MarkEventSeen(key, repoKey, ann.Kind, ev.CreatedAt); err != nil {
```
to:
```go
if err := f.db.MarkEventSeen(key, repoKey, ann.Kind, ann.RefID, ann.Message, ev.CreatedAt); err != nil {
```
(`ann` is already computed above this line via `ExtractAnnouncement`, so no reordering
needed — `ann.Message` here is the pre-collapse, per-event message, which is what should
be stored for search: collapsing only happens afterward, for broadcast display, and must
not affect what's persisted for `!gh search`.)

Also update `allowedEventTypeSet` (fetcher.go:321-337) to map the three new filter strings:
```go
case "issues":
    want["IssuesEvent"] = true
case "create":
    want["CreateEvent"] = true
case "delete":
    want["DeleteEvent"] = true
```

---

## 3. `irc/` package changes — the `!gh` command family

### 3.1 Wiring: `irc/bot.go` + `main.go`

Add to `Bot` struct (near `progtodoDB`):
```go
githubFetcher *github.Fetcher
```
Add setter (same pattern as `SetProgtodoDatabase`):
```go
// SetGitHubFetcher sets the GitHub tracker fetcher for !gh commands.
func (b *Bot) SetGitHubFetcher(f *github.Fetcher) {
    b.githubFetcher = f
}
```
`main.go`: after `githubFetcher := github.NewFetcher(cfg, bot, githubDB, ghCryptor)`
(main.go:352), add `bot.SetGitHubFetcher(githubFetcher)` — before `rstate := &rehashState{...}`.
No separate `SetGitHubCryptor` is needed: `github.Fetcher` already exposes
`EncryptToken`/`DecryptToken` (fetcher.go:175-176), which is the existing convention the
web handlers use instead of holding a raw `*Cryptor` — `irc/` should do the same
(`b.githubFetcher.EncryptToken(plain)`).

### 3.2 New file `irc/github_admin.go` — pending-token state machine + WHOIS secure-check

New per-Bot in-memory state (added to `Bot` struct, guarded by its own mutex, mirroring
`loggedInAdmins`/`loginsMu`):
```go
// pendingGHTokens tracks an in-flight "send me your PAT in this PM" request, keyed like
// adminSessionKey(network, nick). Expiry is enforced both by a timer (ghTokenTimeout) and
// by checking time.Now().Before(deadline) on receipt, in case the timer goroutine is slow
// to fire under load.
pendingGHTokens map[string]pendingGHTokenRequest
ghTokenMu       sync.Mutex

type pendingGHTokenRequest struct {
    owner, repo string
    channels    []string
    eventTypes  []string
    deadline    time.Time
    cancel      func() // stops the expiry timer if the token arrives first
}
```
`ghTokenTimeout = 120 * time.Second` (per product decision).

**WHOIS secure-check** (`irc/github_admin.go`):
```go
// whoisSecureTimeout bounds how long !gh add --private waits for RPL_WHOISSECURE (671)
// before concluding the connection can't be verified as secure. Chosen to comfortably
// exceed round-trip latency to any IRC server while not leaving the admin waiting long.
const whoisSecureTimeout = 5 * time.Second

// checkWhoisSecure issues a WHOIS for nick and reports whether the server confirmed a
// secure connection (numeric 671) before whoisSecureTimeout or RPL_ENDOFWHOIS (318)
// arrives, whichever is first. Not all ircds send 671 even over TLS (it's opt-in server
// support) — a false/negative result here means "couldn't verify", not "definitely
// insecure", and callers must treat it as fail-closed (refuse the token flow) per product
// decision, not silently proceed.
func (b *ircNetwork) checkWhoisSecure(nick string) bool {
    resultCh := make(chan bool, 1)
    var once sync.Once
    send := func(v bool) { once.Do(func() { resultCh <- v }) }

    secureID := b.conn.AddCallback("671", func(e ircmsg.Message) {
        if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], nick) {
            send(true)
        }
    })
    endID := b.conn.AddCallback("318", func(e ircmsg.Message) {
        if len(e.Params) >= 2 && strings.EqualFold(e.Params[1], nick) {
            send(false)
        }
    })
    defer b.conn.RemoveCallback(secureID)
    defer b.conn.RemoveCallback(endID)

    if err := b.conn.Send("WHOIS", nick); err != nil {
        return false
    }
    select {
    case v := <-resultCh:
        return v
    case <-time.After(whoisSecureTimeout):
        return false
    }
}
```
Concurrency note: this is only ever invoked from the admin-gated `!gh add ... --private`
handler, and only one such flow is in-state per (network, nick) at a time (enforced by
`pendingGHTokens`), so overlapping WHOIS replies for the *same* nick from two concurrent
callers isn't a real scenario in practice; still, the `once.Do` above makes it safe even
if it were.

**Token receipt hook**: `dispatchCommand`/`handleCommand` currently only reacts to
messages starting with `b.pfx()`. The bare-token PM reply won't start with `!`, so it
needs a check *before* the `pfx()` gate — in `ircNetwork.dispatchCommand` (irc/network.go's
PRIVMSG callback already calls this), add, before the `strings.HasPrefix(message, b.pfx())`
early return:
```go
if b.tryConsumePendingGitHubToken(target, message, sender) {
    return
}
```
`tryConsumePendingGitHubToken(target, message, sender string) bool`: looks up
`pendingGHTokens[adminSessionKey(b.name, sender)]`; if absent, returns false immediately
(near-zero cost on the hot path for every non-matching PRIVMSG). If present: verify
`isPM := !strings.HasPrefix(target, "#")` (must still be the PM channel) and
`time.Now().Before(req.deadline)`; if either check fails, send a NOTICE explaining why
(expired / must be in PM) and delete the pending entry; otherwise treat `message` as the
raw PAT, call `req.cancel()`, delete the pending entry, and proceed to the shared
"finish adding the repo" logic (§3.4) with the plaintext token — encrypt immediately via
`b.githubFetcher.EncryptToken(message)` and never log/echo the plaintext anywhere
(no `logger.LogChannelEvent` for this specific PRIVMSG — special-case it to skip that
call too, since the current callback in `irc/network.go:406-415` unconditionally logs
every non-CTCP PRIVMSG to the per-channel/per-PM log file before dispatch; this is a
plaintext-secret leak into `logs/` that must be suppressed for this one message. Concretely:
move the `logger.LogChannelEvent(...)` call in `irc/network.go`'s PRIVMSG handler to *after*
`n.dispatchCommand(...)` returns and skip it when `dispatchCommand` reports "this message
was a consumed pending-token reply" — simplest is to have `tryConsumePendingGitHubToken`
be called from the PRIVMSG callback itself, before the existing log line, not from inside
`dispatchCommand`).

### 3.3 `!gh` command block — placement in `handleCommand`

Add a new top-level block in `irc/bot.go`'s `handleCommand`, alongside `!ticket`'s
self-contained admin gate (own `HasPrefix` + own admin check, not nested in the big
shared `if isAdmin && isLoggedInAdmin` block — same rationale as `!ticket`: distinct
subcommands need slightly different messaging):
```go
if strings.HasPrefix(message, b.pfx()+"gh") {
    if !isAdmin || !isLoggedInAdmin {
        b.sendPrivmsg(target, fmt.Sprintf("@%s: Authorized admins only.", sender))
        return
    }
    if b.githubFetcher == nil {
        b.sendPrivmsg(target, "GitHub tracker not initialized.")
        return
    }
    b.handleGHCommand(target, message, sender, source, isPM-computed-here)
    return
}
```
Actual subcommand parsing/dispatch lives in `irc/github_admin.go` as
`func (b *ircNetwork) handleGHCommand(target, message, sender, source string)`, using the
same "trim prefix, split first word uppercased, switch" template as `!bookmark`
(irc/bot.go:946-964):
```go
body := strings.TrimSpace(strings.TrimPrefix(message, b.pfx()+"gh"))
// split first word (subcommand) from rest
sub, rest := splitFirstWord(body) // reuse or write a tiny local helper matching bookmark's inline logic
switch strings.ToUpper(sub) {
case "LIST":   b.ghList(target, sender, rest)
case "ADD":    b.ghAdd(target, sender, source, rest)
case "DEL", "REMOVE": b.ghDel(target, sender, rest)
case "SEARCH": b.ghSearch(target, sender, rest)
default:
    b.sendPrivmsg(target, fmt.Sprintf(
        "Usage: %sgh list | %sgh add <owner>/<repo> [network:#chan ...] [--private] | %sgh del <owner>/<repo> | %sgh search <owner/repo> <query>",
        b.pfx(), b.pfx(), b.pfx(), b.pfx()))
}
```

### 3.4 `!gh list`

```go
func (b *ircNetwork) ghList(target, sender, rest string)
```
No args: iterate `b.getCfg().GitHubTracker.Repos`, one line per repo (same "manual loop +
`time.Sleep`" convention as `!ticket pending`/`!news`, not `sendPrivmsgMentionedLines`,
since this is itemized data, not a wrapped paragraph):
```go
for _, r := range b.getCfg().GitHubTracker.Repos {
    tok := "no token"
    if r.TokenEncrypted != "" { tok = "token set" }
    b.sendPrivmsg(target, fmt.Sprintf("- %s  channels=%s  events=%s  %s",
        r.FullName(), strings.Join(r.Channels, ","), eventTypesOrAll(r.EventTypes), tok))
    time.Sleep(500 * time.Millisecond)
}
```
Reuse `b.githubFetcher.RepoStatuses()` to optionally append OK/Error per repo (mirrors the
web dashboard's `githubRepoRowFrom`).

### 3.5 `!gh add`

```go
func (b *ircNetwork) ghAdd(target, sender, source, rest string)
```
Parse `rest` into fields: first field is `owner/repo`, remaining fields are either
`network:#chan` channel entries or the literal flag `--private`. Split on that flag
first (`strings.Fields` then filter).

**Public/no-token path** (no `--private` flag):
1. Split `owner/repo` on `/`; reject if malformed.
2. Clone `b.GetConfig()`, check `config.FindGitHubTrackerRepo` for a dup — if found, reply
   "already tracked" and return.
3. Append a new `config.GitHubTrackerRepoConfig{Owner, Repo, Channels: parsedChannels}`
   (no `TokenEncrypted`).
4. `config.ValidateConfig(clone)`; on error, reply the error text and return (don't save).
5. `config.SaveConfig(config.DefaultConfigPath, clone)`.
6. `b.RunRehash(fmt.Sprintf("irc admin %s (!gh add)", sender))`.
7. For each parsed channel, `b.JoinChannelSession(network, config.IRChannel{Name: ch})`
   (best-effort, log-only on error — mirrors `web/github_tracker.go`'s `joinAnnounceChannels`;
   consider factoring that exact function out of `web/` into a shared location, e.g.
   `config` or a small new `botutil`, so both web and IRC call the same code instead of
   duplicating the network-split + best-effort-join loop — flagged as a nice-to-have
   refactor, not required for correctness).
8. Reply with confirmation: `"Tracking <owner/repo> (public). Channels: ...".`

**Private/`--private` path**:
1. Refuse immediately, before anything else, if the invoking message's `target` is a
   channel (`strings.HasPrefix(target, "#")`) — reply in the channel only with:
   `"@sender: Private-repo tokens can only be added in a PM to me — resend this in a query."`
   and stop. Never proceed to WHOIS or any pending-state setup from a channel invocation.
2. If in PM: call `secure := b.checkWhoisSecure(sender)`. If `!secure`, reply (via
   `sendNotice`, in the PM):
   `"Can't verify this connection is encrypted (no secure-connection confirmation from the server within 5s). Add private-repo tokens via the web dashboard instead."`
   and stop — do not create a pending-token entry.
3. If secure: validate `owner/repo` isn't already tracked (same check as public path,
   without saving yet), then register `pendingGHTokens[adminSessionKey(b.name, sender)]`
   with the parsed owner/repo/channels/eventTypes and a `time.AfterFunc(ghTokenTimeout, ...)`
   that deletes the pending entry and sends a PM/NOTICE "timed out, no token received" if
   still present when it fires.
4. Reply (NOTICE, in the PM): `"Reply in this PM with the PAT for <owner>/<repo> within 120s. It will not be echoed or logged."`
5. On receipt (§3.2's `tryConsumePendingGitHubToken`): run steps 2-8 of the public path
   above, but with `TokenEncrypted: encryptedToken` set on the new entry, and a final
   confirmation reply that explicitly does *not* echo the token: `"Tracking <owner/repo> (private, token stored encrypted). Channels: ..."`.

### 3.6 `!gh del`

```go
func (b *ircNetwork) ghDel(target, sender, rest string)
```
Parse `owner/repo`; if not found in config, reply "not tracked"; else clone config, filter
it out, `ValidateConfig` (trivially passes — removing a repo can't introduce new invalid
state), `SaveConfig`, `RunRehash`, reply confirmation. Does **not** part any channels (same
as the web DELETE handler, which also doesn't part channels — leaving that as a manual
follow-up for the admin is consistent with existing behavior).

### 3.7 `!gh search` — smart dispatch

```go
func (b *ircNetwork) ghSearch(target, sender, rest string)
```
1. Parse `rest` into `ownerRepo, query` (first field, remainder joined back with spaces
   for the text-search case — a query might contain spaces).
2. Look up the repo in config (`config.FindGitHubTrackerRepo`); if not tracked, reply
   "not a tracked repo" (search only makes sense against a repo we already have a token/
   dedup-cache for, and reusing an already-configured repo avoids the admin having to
   re-supply auth for a one-off lookup).
3. Decrypt token via `b.githubFetcher.DecryptToken(r.TokenEncrypted)` (empty string if none
   — fine for a public repo).
4. Dispatch on `query`:
   - `^#?\d+$` → strip leading `#`, parse int `n`. Call
     `github.FetchPullRequest(ctx, owner, repo, token, n)`. If non-nil result, format and
     reply as `[PR #n] title state 🔗 url`. If nil (404) or error is a 404, call
     `github.FetchIssue(ctx, owner, repo, token, n)` and format as `[ISSUE #n] ...` if
     found; if neither found, reply "no PR or issue #n found in owner/repo".
   - `^[0-9a-f]{7,40}$` (case-insensitive; `regexp.MustCompile("(?i)^[0-9a-f]{7,40}$")`) →
     `github.FetchCommit(ctx, owner, repo, token, query)`; reply `[COMMIT <short>] message
     by author 🔗 url` or "no commit found".
   - otherwise → local search: `pattern, err := regexp.Compile("(?i)" + query)`; on error,
     fall back to `regexp.Compile("(?i)" + regexp.QuoteMeta(query))` (documented literal-
     search fallback from §2.4). Call `b.githubFetcher.DB().SearchEvents(repoKey, pattern, 5)`
     (requires exposing the fetcher's `*Database` — add a small accessor
     `func (f *Fetcher) DB() *Database { return f.db }` to `github/fetcher.go`, since
     `Fetcher.db` is currently private and IRC has no other path to it). Reply one line per
     match (manual loop + `time.Sleep`, same convention as §3.4), or "no matches" if empty.
5. This entire command's HTTP-lookup branches must run with a bounded `context.WithTimeout`
   (e.g. 15s, matching `eventsHTTPClient.Timeout`) — same reasoning as every other outbound
   HTTP call in this repo (CLAUDE.md "Development Workflow" / existing timeout convention).
   Since `dispatchCommand` already runs each command on its own goroutine
   (`irc/network.go`'s `dispatchCommand`/`cmdSem`), a slow GitHub API call here doesn't block
   the read loop — no additional goroutine needed inside `ghSearch` itself.

---

## 4. `internal/ircusage/print.go` + `irc/bot.go` `!help`

### `internal/ircusage/print.go`
Add to `adminLines`:
```go
{cmd: "!gh list", desc: "List tracked GitHub repos (channels, event filter, token state)"},
{cmd: "!gh add <owner>/<repo> [net:#chan ...] [--private]", desc: "Track a repo; --private prompts for a PAT via PM (requires a verifiably secure/TLS connection)"},
{cmd: "!gh del <owner>/<repo>", desc: "Stop tracking a repo"},
{cmd: "!gh search <owner>/<repo> <query>", desc: "PR/issue # or commit SHA -> live GitHub lookup; else local text/regex search over recent events"},
```

### `irc/bot.go` `!help`
The admin summary string (bot.go:496-497) is built independently — append to it:
```go
admin := fmt.Sprintf("Admin: ... %sticket pending/approve/cancel [ID], %sgh list/add/del/search",
    ..., b.pfx())
```
(Keep it terse — this line is already long; don't spell out `--private` here, just point
at `!help` → full usage / ircusage doc for details.)

---

## 5. CLAUDE.md updates

In the "GitHub Tracker: token scope for private repos" section:
- Update the opening sentence: the tracker now also parses `IssuesEvent`/`CreateEvent`/
  `DeleteEvent` from the same `/events` poll, and separately, `!gh search` makes **on-demand**
  calls to `GET /repos/{owner}/{repo}/pulls/{number}`, `.../issues/{number}`, and
  `.../commits/{sha}` — these are not part of the polling cycle and don't consume the same
  rate-limit budget check used to back off `Fetch()` (clarify this doesn't get the same
  "pause remaining repos" backoff `fetcher.go:264-267` applies — a burst of `!gh search`
  calls has no repo-list-wide backoff at all today; note as a known limitation, not fixed
  in this plan).
- Add a new fine-grained PAT scope bullet: **Issues** (read) — "covers `IssuesEvent` and
  the `!gh search` issue-number lookup fallback."
- Document `!gh add ... --private`'s PM-only, WHOIS-671-gated token flow, and that it's the
  only way to add a token from IRC (there is no `!gh` equivalent of editing an existing
  repo's token — that remains dashboard-only for now).
- Note the new `event_types` filter strings (`issues`, `create`, `delete`) alongside the
  existing three.

---

## 6. Testing

### `github/events_test.go`
- `TestExtractAnnouncement_Issue_OpenedAndClosed` / `_ActionNotAnnounced` (e.g. "labeled").
- `TestExtractAnnouncement_Create_Branch` / `_Tag` / `_RefTypeRepositoryIgnored`.
- `TestExtractAnnouncement_Delete_Branch` / `_Tag`, asserting **no** 🔗 link in the message.
- Update existing push/PR/release assertions for the new `RefID` field and the tag-text
  change (e.g. body no longer contains `"PR #12"` — that moved to the tag).
- `TestCollapseForBroadcast_*` — add a case mixing `issues`/`create`/`delete` kinds to
  confirm each still gets its own collapsed line and `kindLabel` pluralization.

### New `github/format_test.go` (doesn't exist today — add it)
- One test per `format*` function asserting exact tag text, e.g.
  `formatPush(..., "06dfee0")` contains `[PUSH 06dfee0]`; `formatDelete` contains no
  `🔗` byte sequence at all.

### `github/db_test.go`
- Extend `TestEventSeen_DedupRoundTrip`-style test for the new `MarkEventSeen` signature
  (assert `ref_id`/`message` round-trip).
- New `TestSearchEvents_MatchesMessageOrRefID`, `TestSearchEvents_RespectsLimit`,
  `TestSearchEvents_FiltersByRepo`, `TestSearchEvents_CaseInsensitive`.
- Migration-safety test: open a DB seeded with the *old* schema (no `ref_id`/`message`
  columns) and confirm `NewDatabase` on it doesn't error and old rows read back with empty
  string for the new columns (mirrors how `rss/db.go`'s migrations are exercised, if such a
  test exists there — check for a pattern to copy).

### `github/client_test.go`
- `TestFetchPullRequest_200`, `_404ReturnsNilNilNotError`, `_UsesBearerAuthWhenTokenSet`.
- `TestFetchIssue_200`, `_404ReturnsNilNil`.
- `TestFetchCommit_200`, `_404ReturnsNilNil`.
- Reuse `withTestServer` helper as-is.

### `github/fetcher_announce_test.go`
- Extend `TestAnnounceNewEvents_DedupAndEventTypeFilter` (or add a sibling test) for the
  three new `EventTypes` filter strings via `allowedEventTypeSet`.
- New test asserting `announceNewEvents` passes through `ann.RefID`/`ann.Message`
  (pre-collapse) into `MarkEventSeen`, not the collapsed/suffixed message.

### `config/github_tracker_test.go` / `config/validate.go` tests
- Extend `validateGitHubTracker` coverage for the three new filter strings (valid + one
  invalid).

### `irc/` package (new tests)
- No existing test file covers `handleCommand`'s admin subcommands directly (per the file
  listing, `irc/` tests are narrowly scoped: `permissions_test.go`, `cfg_race_test.go`,
  etc. — there's no `bot_test.go` exercising PRIVMSG end-to-end). Given that pattern,
  keep new IRC-side tests narrowly scoped too rather than trying to stand up a full fake
  IRC connection:
  - A pure-function test for whatever small parsing helpers `ghAdd`/`ghSearch` end up using
    (e.g. splitting `owner/repo`, detecting the numeric/hex/text query-dispatch regexes) —
    these can be extracted as standalone functions and tested without any IRC plumbing.
  - `checkWhoisSecure` is fundamentally hard to unit test without a fake `ircevent.Connection`
    (it needs a real `AddCallback`/`Send` pair) — flag this as **not practically unit-testable
    in isolation** given this codebase's current test infrastructure; rely on manual/staging
    verification against a real ircd (Libera.Chat/OFTC both send 671) instead, and note this
    explicitly as an accepted gap rather than silently skipping it.

---

## Open questions / risks to confirm before implementation

1. **`RefID` for push events on a 0-commit-payload push** (GitHub's trimmed Events API,
   per `extractPush`'s existing comment) — `p.Head` is still always populated even when
   `Commits` is empty, so the short-SHA tag works either way; confirmed by re-reading
   `extractPush`'s existing `if err != nil || p.Head == ""` guard.
2. **RPL_WHOISSECURE (671) is genuinely opt-in per-ircd** — confirmed Libera.Chat/OFTC send
   it; smaller/older ircds may not. The fail-closed design (treat "no 671 within timeout"
   as "not secure") means on such a network `!gh add --private` will *always* refuse, even
   over a real TLS connection — this is the deliberate, product-decided tradeoff, but worth
   flagging back to the user as a real operational limitation for less mainstream networks.
3. **`!gh add` public path currently has no rate limit** on how many repos an admin can
   add — matches the web dashboard's own lack of a cap, so not a regression, just noting
   it's unbounded by this plan too.
4. Whether `!gh del` should also offer to `PartChannelSession` any channels that were
   session-joined solely for that repo — left as explicitly out of scope (matches existing
   web DELETE behavior).

---

### Critical Files for Implementation
- `github/events.go`
- `github/format.go`
- `github/db.go`
- `github/fetcher.go`
- `irc/bot.go`
- `irc/network.go`
- `github/client.go`
