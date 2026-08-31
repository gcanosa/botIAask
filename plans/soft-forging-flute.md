# Multi-network IRC support + web GUI network management

## Context

botIAask currently connects to exactly one IRC server. `config.IRCConfig` is a single
struct (`config/config.go:98-108`), `irc.Bot` wraps exactly one `*ircevent.Connection`
(`irc/bot.go:36-109`), and `main.go` starts one `Bot.Start()` goroutine that blocks
forever. Multiple *channels* on that one network are already supported and work well
(`IRCConfig.Channels []IRChannel`, with a web CRUD UI at `/api/irc/channels*`).

The user wants the bot to connect to **multiple IRC networks/servers simultaneously**,
each auto-joining its own channels, answering commands and broadcasting (RSS, etc.)
independently per network — and wants the existing web dashboard extended so networks
themselves (not just channels) can be added/edited/removed, live, without restarting
the bot.

This turns "reconnect required" (today's rehash behavior for server/port/nick/TLS
changes, `irc/bot.go:441-443`) into a real per-network connect/disconnect/reconnect
capability, which is *necessary* for the web UI to be usable (editing a network that
then says "now restart the bot" is a bad experience).

Scope decision (confirmed with user): nick-keyed state that could collide across
networks (admin login sessions, `!tell`, `!seen`) gets a `network` dimension. The
`ignoreList` stays global (nick ignored everywhere) — a deliberate, documented
simplification; not worth the complexity for a moderation footgun list.

## Architecture

**Key mechanism: `ircNetwork` embeds `*Bot`.** Move all per-connection state (the
`*ircevent.Connection`, channel-membership map, session joins, connected/authenticated
flags) off `Bot` onto a new `ircNetwork` struct. Keep everything genuinely global
(AI client, rate limiter, admin-login map, DB handles, `cmdSem`) on `Bot`. Because
`ircNetwork` embeds `*Bot`, every existing `Bot` method (`getCfg()`, `pfx()`, `cmd()`,
`sanitize()`, `IsAdmin()`, …) is promoted for free — **no call-site changes needed for
those**. `Bot` becomes a manager holding `networks map[string]*ircNetwork`.

The one mechanical, repeated change across ~8 command-handler files
(`irc/bot.go`, `social.go`, `currency.go`, `flight_cmd.go`, `movie_cmd.go`,
`ping_cmd.go`, `weather_cmd.go`, `worldtime.go`) is: **any method whose body touches
connection-scoped state (`conn`, `channelMembers`, `sessionJoins`, or calls
`sendPrivmsg`/`sendNotice`/`dispatchCommand`/`handleCommand`) changes its receiver
from `(b *Bot)` to `(b *ircNetwork)`.** Keep the receiver variable name `b` — method
bodies are otherwise untouched. Example (`irc/flight_cmd.go:19`):

```go
// before: func (b *Bot) handleFlightCommand(target, sender, rest string) {
// after:  func (b *ircNetwork) handleFlightCommand(target, sender, rest string) {
```

The rest of that method's body (`b.sendPrivmsg(...)`, `b.getCfg()...`, `b.pfx()`)
needs zero edits.

## 1. Config (`config/`)

Replace `IRCConfig` (config.go:98-108) with a network list:

```go
type IRCConfig struct {
    Networks []IRCNetworkConfig `yaml:"networks"`
}

type IRCNetworkConfig struct {
    Name        string         `yaml:"name"`   // unique short label; defaults to Server if omitted
    Server      string         `yaml:"server"`
    Port        int            `yaml:"port"`
    UseSSL      bool           `yaml:"use_ssl"`
    Nickname    string         `yaml:"nickname"`
    Channels    []IRChannel    `yaml:"channels"`
    QuitMessage string         `yaml:"quit_message,omitempty"`
    Services    ServicesConfig `yaml:"services"`
}
```

- Add `FindIRCNetworkByName`, `IRCNetworkNames` helpers (new `config/irc_network.go`),
  mirroring `FindIRChannelByName`/`IRChannelNames` in `config/irc_channel.go`.
- Add a custom `UnmarshalYAML` on `IRCConfig` (new `config/irc_config.go`), following
  the same "accept old shape or new shape" convention `IRChannel.UnmarshalYAML`
  already uses (`config/irc_channel.go:61-98`): if the `irc:` mapping has a `networks:`
  key, decode the list; otherwise decode the legacy flat fields directly into one
  `IRCNetworkConfig` and wrap it in a single-element slice. This keeps every existing
  `config.yaml` working unedited. No custom `MarshalYAML` needed — plain
  `yaml.Marshal` of the new struct already produces the new `networks:` shape, so
  `SaveConfig` (config.go:282-294) always writes the new format (one-way migration).
- Add `ValidateConfig(cfg *Config) error` (new `config/validate.go`) — no `Validate()`
  exists anywhere today. Check: at least one network, every network has a non-empty
  unique (case-insensitive) `Name` with no `:` in it (needed for the RSS prefix
  convention below). Call it from `LoadConfig` (config.go:215-235) and from the new
  web network-CRUD handlers before `SaveConfig`.
- `config/rehash_diff.go`: `IRCEndpointChanged` (rehash_diff.go:34-43) and the IRC
  block of `RehashDiff` (rehash_diff.go:62-77) become per-network (loop networks by
  name, diff added/removed/endpoint-changed/channels-changed). Diff text changes
  from "reconnect required; not hot-applied" to "(reconnecting)" since it's now
  actually hot-applied. Update `config/rehash_diff_test.go`.
- `config/config.yaml.template`: replace the flat `irc:` block with a `networks:`
  list (one example entry), and a comment noting the legacy flat shape still loads.
- `CloneConfig` (rehash_diff.go:12-32) needs no change — YAML round-trip works as-is.

## 2. `irc` package refactor

**`irc/network.go` (new)** — the `ircNetwork` struct:

```go
type ircNetwork struct {
    *Bot
    name string

    conn *ircevent.Connection

    statsMu        sync.Mutex // guards connected/connectionTime, per network
    connected      bool
    connectionTime time.Time

    channelMembers map[string]map[string]struct{}
    membersMu      sync.RWMutex

    sessionJoins   []config.IRChannel
    sessionJoinsMu sync.Mutex

    authenticated bool
    authMu        sync.RWMutex
}

func (b *ircNetwork) netCfg() config.IRCNetworkConfig {
    nc, _ := config.FindIRCNetworkByName(b.getCfg().IRC.Networks, b.name)
    return nc
}
```

`Bot` (bot.go:36-109) drops `conn`, `channelMembers`/`membersMu`,
`sessionJoins`/`sessionJoinsMu`, `authenticated`/`authMu`, `connected`/`connectionTime`
(those move to `ircNetwork`). `Bot` gains:

```go
networks   map[string]*ircNetwork
networksMu sync.RWMutex

// loggedInAdmins is now keyed "<network>\x00<foldednick>", not bare nick.
loggedInAdmins map[string]bool
```

**`Start()` / `connectNetwork()`** — replace the monolithic `Start()` (bot.go:559-868).
`Bot.Start()` loops `b.getCfg().IRC.Networks`, launches one
`guard.Go("irc:"+netCfg.Name, func() { ... })` goroutine per network (reusing
`internal/guard`, already importable from `irc/` — same module), each building an
`ircNetwork`, registering the same event callbacks as today (PRIVMSG/NOTICE/JOIN/
PART/KICK/QUIT/NICK/connect/disconnect — bodies unchanged except `b.` → `n.` and
`b.getCfg().IRC.Server` → `n.name` wherever it's used as the logger's network label),
connecting with the existing backoff-retry loop, then calling `n.conn.Loop()`
(blocks until that network's `Quit()`). `Bot.Start()` waits on a `sync.WaitGroup`
for all network goroutines. `main.go`'s call site (`guard.Go("irc bot", bot.Start)`,
main.go:378-382/430-434) needs **no change** — this is all internal to `irc`.

`ircJoinWithKey(conn *ircevent.Connection, ch config.IRChannel) error`
(bot.go:230-235) is already connection-parameterized; reuse as-is.

**Live add/remove/reconnect — replaces `ApplyLiveConfig` (bot.go:421-467).** This is
the key new capability the web UI needs. Diff `oldCfg.IRC.Networks` vs
`newCfg.IRC.Networks` by `Name` using the existing `channelListDifference` helper
(bot.go:246-254, already a generic string-set diff):

- Name in new, not old → connect it (spawn `ircNetwork` + goroutine, same path as
  `Start()`, register in `b.networks`).
- Name in old, not new → `net.conn.QuitMessage = ...; net.conn.Quit()` and remove
  from `b.networks` (the owning goroutine's `Loop()` returns on its own).
- Name in both, `Server`/`Port`/`Nickname`/`UseSSL`/`Services` unchanged → hot
  diff+apply the channel auto-join list on the existing connection (today's logic,
  bot.go:455-465, unchanged, just scoped to that one network's channels).
- Name in both, any endpoint field changed → disconnect + reconnect just that one
  network (same as "removed" then "added" for that name). This replaces the old
  "log a warning, no-op" behavior (bot.go:441-443) — it's what makes editing a
  network's server/port/nick/SASL in the web UI actually take effect live.

**Command-handler receiver migration** — see Architecture section above for the
pattern. Apply it to every `handle*Command` method and to `dispatchCommand`,
`handleCommand`, `sendPrivmsg`, `sendNotice`, `handleCTCPRequest`,
`sendPrivmsgMentionedLines`, `JoinChannelSession`/`PartChannelSession`/
`ListSessionChannels`, and the `isUserOnline`/`deliverTells` helpers in
`irc/social.go` (both touch `channelMembers`/`sendPrivmsg`). Do this file-by-file,
recompiling after each — the compiler reliably flags every remaining `(b *Bot)`
method that references now-nonexistent `Bot` fields, which is your checklist.

`sendPrivmsg`/`sendNotice` also get the "network label instead of server hostname"
replacement everywhere they call `logger.LogChannelEvent(...)`:

```go
// before: logger.LogChannelEvent(b.getCfg().IRC.Server, target, ...)
// after:  logger.LogChannelEvent(b.name, target, ...)
```

(repeats at every `LogChannelEvent` call site inside the moved callbacks —
mechanical, same replacement each time).

**Global aggregates that now loop networks** (`Bot`-level methods, not mechanical —
write these once, explicitly):
- `Broadcast(channels []string, message string)` (bot.go:364-381): each channel
  string may be prefixed `"networkName:#chan"` (see RSS below); parse the prefix
  (falling back to the first configured network for bare `"#chan"`, back-compat),
  look up that network in `b.networks`, and broadcast on it.
- `SendMessage(network, target, message string)` (bot.go:1943-1946, used by `web`)
  — gains a `network` parameter; empty string falls back to the first network
  (needed for old `uploads` rows created before the network column existed).
- `IsConnected()`/`IsAuthenticated()` (bot.go:351-362) — become "any network"
  aggregates for existing back-compat callers.
- New `NetworkStatuses() []NetworkStatus` (name/server/connected/authenticated/
  nickname/channel count) for the dashboard's per-network view.
- `RequestQuit` (bot.go:326-336), `NotifyAdmins`/`NotifyLoggedInAdminsNotice`/
  `NotifyLoggedInAdminsRehashSummary` (bot.go:1897-1941) — loop all networks;
  admin notices go to the network the admin is logged in on (via the new
  `network\x00nick`-keyed `loggedInAdmins`), not blasted to every network.
- `startReminderScheduler` (social.go:187-216) stays on `*Bot`, loops networks
  trying `net.isUserOnline(nick)` (first match wins — matches today's
  single-network semantics).

**Logger fix (`logger/channel_key.go`)** — `ChannelFileKey(channel, network string)`
currently only uses its second param as a fallback for non-channel events; for
actual channels it silently drops it, so two networks sharing a channel name would
interleave in one log file today. Fix it to always prefix: `"<network>_<bare-channel>"`
(aligns with the format CLAUDE.md already documents). This is a real correctness fix,
not scope creep — required before multi-network logging is safe. Log filenames change
from `channel1_2026-08-28.log` to `libera_channel1_2026-08-28.log` going forward; see
§4 for read-side backward compat.

## 3. Dependent packages

**RSS** (`rss/fetcher.go:289`, `config/rss_channels.go`) — keep
`RSSConfig.Channels []string` as plain strings, adopt a `"networkName:#chan"` prefix
convention (bare `"#chan"` = first configured network, back-compat). Add
`SplitNetworkChannel`/`JoinNetworkChannel` helpers in `config/rss_channels.go`; used
by `Bot.Broadcast` and by the web RSS-announce toggle. `rss.Fetcher`/`BotInterface`
(rss/fetcher.go:18-21) need no signature change — `Bot.Broadcast` does the parsing.

**Uploads** (`uploads/db.go`) — add `Network string` next to the existing
`Channel string` on the `Upload` struct (db.go:35) and an
`ALTER TABLE ... ADD COLUMN network TEXT DEFAULT ''` migration following the exact
pattern already used for other columns in this file (tolerate "duplicate column").
Thread `Network` through the INSERTs that create upload/paste rows, and update the
~5 `web/server.go` call sites (2278, 2434, 2460, 2675, 2711) from
`s.bot.SendMessage(upload.Channel, ...)` to
`s.bot.SendMessage(upload.Network, upload.Channel, ...)`.

**Bookmarks** (`bookmarks/db.go`) — add `Network string` to `Reminder`, `Tell`,
`Seen` (db.go:30-54) and `ALTER TABLE ... ADD COLUMN network TEXT DEFAULT ''` on
`reminders`/`tells`/`seen`, following the exact `reminders.due_at` migration already
in this file (db.go:93-97). Add composite indexes
(`idx_tells_network_to_fold` etc.). Thread a `network string` parameter through
`AddTell`/`TakeTells`/`AddReminder`/`DueReminders`/`RecordSeen`/`GetSeen` and their
`WHERE` clauses. **One non-trivial piece**: `seen`'s primary key is `nick_fold`
alone (db.go:117) — global across the process today. Making "last seen" genuinely
per-network requires changing the PK to `(network, nick_fold)`, which needs a real
table rebuild (create new table, copy rows, drop, rename) rather than a bare
`ALTER TABLE ADD COLUMN` — call this out and do it deliberately, guarded the way
other structural migrations in this codebase are. Delivery call sites in
`irc/social.go` (`deliverTells`, `recordSeen`, `!tell`/`!seen` handlers) and the
on-JOIN reminder delivery in `bot.go` (~707-721) pass `b.name` as the network arg
once those methods are already `*ircNetwork` receivers.

## 4. Web dashboard (`web/`)

**New "IRC Networks" CRUD**, modeled directly on the existing channels handler
(`handleIRCChannels`, server.go:1282-1410) — same guard pattern
(`requireAdminCSRF`), same persist-then-rehash flow
(`config.SaveConfig` → `s.runFullRehashFromWeb(...)`, which now live-applies via §2):

- `GET/POST/DELETE /api/irc/networks` — list (joined with `Bot.NetworkStatuses()`
  for live connected/authenticated state, same join pattern GET already uses for
  session channels at server.go:1300-1306), add, remove by `?name=`. Reject removing
  the last remaining network (`ValidateConfig` would reject it anyway — surface as
  a clean 400).
- `PUT /api/irc/networks/edit` — edit server/port/use_ssl/nickname/quit_message/
  services for an existing network (name itself immutable via this endpoint —
  renaming is remove+add in the UI).

**Existing channel endpoints gain a `network` selector** — `handleIRCChannels` and
the reveal/announce/autojoin/session variants (server.go:1282-1868) all currently
read/write the single `cfg.IRC.Channels`. Add `network` (query param for GET/DELETE,
JSON field for POST/PUT), default to the first configured network when absent
(back-compat), look up the network by name, and operate on
`cfg.IRC.Networks[i].Channels` instead. `JoinChannelSession`/`PartChannelSession`/
`ListSessionChannels` calls gain the network argument per the new `Bot` signatures.

**`/api/status`** — replace the single `"server"`/`"nickname"`/`"channels"` fields
with `"networks": s.bot.NetworkStatuses()`; keep an aggregate `"connected"` boolean
for existing callers (`handleHealth` needs no change).

**`web/logs_api.go` backward compat** — `parseLogBaseName`/`parseArchiveName`
(logs_api.go:44-68) already treat the whole pre-date portion as an opaque key, so
they parse both old (`channel1_2026-08-28.log`) and new
(`libera_channel1_2026-08-28.log`) filenames without changes. `handleLogCatalog`'s
correlation step (logs_api.go:141-145) needs to compute both the new network-prefixed
key and the legacy bare key per configured channel and merge their disk-date sets,
so history logged before this change stays browsable. `handleLogHistory` needs a
fallback: try the new-format key first, fall back to the legacy bare key if the file
doesn't exist for that date.

**Frontend** (`web/templates/index.html`, `app.js`) — new "IRC Networks" card above
the existing `#irc-autojoin-wrap` (index.html:747-795), same visual shape (add-form +
table with Edit/Remove actions, Connected/Authenticated badges). A network `<select>`
scopes the existing channels card (`fetch('/api/irc/channels?network=' + selected)`).
`app.js` gets `fetchIRCNetworks`/`ircNetworksRender`/`ircNetworkAdd`/
`ircNetworkRemove`/`ircNetworkEdit`, mirroring `fetchIRCAutojoin`/`ircAutojoinRender`/
`ircAutojoinAdd`/`ircAutojoinRemove` (app.js:2404-2727) function-for-function. The
existing autojoin fetch/add/remove/toggle calls (app.js:2433, 2564, 2586, 2607, 2631,
2653, 2671, 2702) each gain a `network` field/query-param — same one-line addition
repeated ~7 times.

## 5. `main.go`

No signature changes needed anywhere in `main.go` — `irc.NewBot(cfg, aiClient)`,
`guard.Go("irc bot", bot.Start)`, `bot.RequestQuit("")`, and the `rehashState`/
`doApplyRehash` wiring all keep their existing call shapes; the new behavior is
internal to `irc.Bot`. Treat any `main.go` compile break as a signal that a public
`Bot`/`Server` method signature changed in a way this plan didn't account for.

## Suggested order

1. **Config** (`config/irc_network.go`, `irc_config.go`, struct change, validate,
   rehash_diff, template). Update `config/rehash_diff_test.go`/`cfg_race_test.go`
   literals; add `config/irc_config_test.go` (legacy-decode round-trip, new-shape
   round-trip, duplicate-name validation). `go test ./config/...` green before
   moving on — the rest of the module won't compile yet, that's expected.
2. **`irc` package** — `network.go`, `Bot` struct trim, `Start`/`connectNetwork`,
   live add/remove/reconnect, then the receiver-migration pass file by file
   (`go build ./irc/...` after each file — the compiler's error list is your
   checklist). Update existing `irc` test files' bot construction.
3. **Logger fix** — `logger/channel_key.go`. Confirm one write produces
   `<network>_<channel>_<date>.log`.
4. **Dependent packages** — `bookmarks`, `uploads`, `rss` prefix helpers.
   `go test ./bookmarks/... ./uploads/... ./rss/...`; manually run once against a
   fresh `data/` to confirm migrations apply, and once against a pre-existing DB
   copy to confirm "duplicate column" tolerance doesn't break startup.
5. **`web` package** — new endpoints, `network` param plumbing, `/api/status`,
   `logs_api.go` compat. `go vet ./web/...`, existing web tests green.
6. **Frontend** — Networks card + selector.
7. **`main.go`** — should need ~zero changes if 2 and 5 were done right.

## Verification

- `go build ./...`, `go vet ./...`, `go test ./...` clean across the whole module.
- Two-network smoke test: point `config.yaml` at two networks (e.g. two local
  ngircd instances on different ports, or one local + one public test network)
  with distinct `name`s. Confirm both connect, log to distinct
  `<network>_<channel>_<date>.log` files, and a command (`!weather`, `!ping`)
  issued on one network only replies on that network.
- Web dashboard smoke test (log in as admin): add a third network via the new UI,
  confirm it connects live (`/api/status` `networks[]` + an actual JOIN visible on
  the target server) with no process restart; edit an existing network's
  port/nickname and confirm it reconnects live; remove a network and confirm it
  QUITs cleanly and disappears from the channel-network selector; add/remove a
  channel scoped to one network via the (now network-aware) channels UI and
  confirm it only joins/parts on that network.
- Rehash smoke test: hand-edit `config.yaml` to add a network, `SIGHUP` the
  daemon, confirm the admin NOTICE rehash summary reports the network add and the
  bot actually connects — matching the web-driven path.

## Critical files

- `config/config.go`, `config/irc_channel.go` (pattern to mirror), `config/rehash_diff.go`
- `irc/bot.go` (most of the refactor), `irc/social.go`, plus the ~6 other command-handler files for the mechanical receiver change
- `logger/channel_key.go`
- `bookmarks/db.go`, `uploads/db.go`, `rss/fetcher.go`
- `web/server.go` (channels CRUD to mirror), `web/logs_api.go`
- `web/templates/index.html`, `web/templates/app.js`
