# Per-network IRC admin & ignore-list scoping

## Context

botIAask now connects to multiple IRC networks simultaneously (one `ircNetwork`
per connection, all owned by a single process-wide `*Bot`), but permissions
never got the same treatment: `Bot.IsAdmin` checks one flat, global
`config.Admin.Admins` hostmask list (`irc/bot.go:373-380`), so anyone on that
list is an admin on *every* connected network. The `!ignore` list
(`Bot.ignoreList`, `irc/bot.go:60-61`) is the same — global, keyed only by
lowercased nick, so ignoring a troublemaker on one network silences them
everywhere. The user wants networks to be able to have different admins/users,
i.e. genuinely separate permission scopes per network, matching the "one bot
instance per network" mental model even though it's one process.

Confirmed via exploration: the only piece of admin state that's *already*
per-network is the `!admin` login session (`loggedInAdmins`, keyed by
`adminSessionKey(network, nick)` in `irc/network.go:70-84`) — the underlying
authorization lists (who's *allowed* to log in, who's ignored) are not.

Decisions made with the user:
- Admin scope: **global list stays as a bot-wide fallback**, plus a new
  optional per-network `admins` list that grants admin only on that network.
  Non-breaking for existing configs.
- Ignore list: becomes **per-network** (matches the "different users per
  network" ask and the existing `adminSessionKey` pattern).
- Web dashboard: add per-network scoping to the existing admin-hostmask UI via
  a network selector, mirroring the selector already used for autojoin
  channels.

Per-channel granularity is out of scope — only per-network.

## Config changes

**`config/irc_config.go`** — add an `Admins` field to `IRCNetworkConfig`
(near `Services`, `config/irc_config.go:16-32`):

```go
// Admins are extra hostmask fragments granted admin only on this network, on
// top of the global admin.admins list (which applies to every network).
Admins []string `yaml:"admins,omitempty"`
```

No new validation in `config/validate.go` — the existing global
`Admin.Admins` has none either (no dedup/empty checks), so stay consistent.

**`config/config.yaml.template`** — add a commented example `admins:` entry
under the second (commented-out) network block, next to the existing
top-level `admin.admins` example, so both scopes are documented together.

The ignore list is already in-memory-only (never persisted to config), so no
config schema change is needed for it.

## `irc/` changes

**`irc/network.go`** — add an `IsAdmin` override on `*ircNetwork` that shadows
the promoted `Bot.IsAdmin`, following the exact pattern already used for
`IsAuthenticated` (`irc/network.go:60-68`):

```go
// IsAdmin shadows the promoted Bot.IsAdmin: admin on this network means
// matching the global admin.admins list (bot-wide) OR this network's own
// admins list (network-only, config/irc_config.go IRCNetworkConfig.Admins).
func (b *ircNetwork) IsAdmin(fullHostmask string) bool {
	if b.Bot.IsAdmin(fullHostmask) {
		return true
	}
	for _, admin := range b.netCfg().Admins {
		if strings.Contains(fullHostmask, admin) {
			return true
		}
	}
	return false
}
```

`handleCommand`'s existing `b.IsAdmin(source)` call (`irc/bot.go:477`) already
runs on an `*ircNetwork` receiver, so this starts taking effect with no other
call-site changes. `Bot.IsAdmin` itself (`irc/bot.go:373-380`) is untouched —
it stays the global-only check, reused here and still directly callable from
tests (`irc/cfg_race_test.go:39`).

**`irc/bot.go`** — scope `ignoreList` per network by reusing the existing
`adminSessionKey(network, nick)` composite-key helper (`irc/network.go:72-74`)
instead of a bare lowercased nick:

- `!ignore` handler (`irc/bot.go:596-608`): change
  `b.ignoreList[strings.ToLower(user)] = true` to
  `b.ignoreList[adminSessionKey(b.name, user)] = true`.
- Silence check (`irc/bot.go:694-698`): change
  `b.ignoreList[strings.ToLower(sender)]` to
  `b.ignoreList[adminSessionKey(b.name, sender)]`.
- `!stats` ignored-count (`irc/bot.go:1698`, `len(b.ignoreList)`): unchanged —
  it becomes a bot-wide total across all networks' ignore entries, which is
  still a meaningful number for that display.

No struct/type changes needed (`ignoreList` stays `map[string]bool`); this is
purely a keying change, mirroring what `loggedInAdmins` already does.

## Web dashboard changes

**`web/server.go`** — extend `handleConfigIRCAdmins` (`web/server.go:1213-1300`)
to accept an optional `network` scope, reusing the exact
resolve-then-locate-by-`EqualFold` pattern already used in
`handleIRCChannels` (`web/server.go:1315-1480`) rather than inventing a new
handler:

- GET: read `network := r.URL.Query().Get("network")`. Empty → return the
  existing global `cfg.Admin.Admins` under `"hostmasks"` (unchanged
  behavior). Non-empty → find the network via `config.FindIRCNetworkByName`,
  404 `"Unknown network"` if missing, return that network's `Admins` list
  instead.
- POST: JSON body gains `Network string \`json:"network"\``. Empty → append
  to global list (unchanged). Non-empty → lock `s.cfgMu`, find the network
  index by `EqualFold` in `s.cfg.IRC.Networks` (same scan `handleIRCChannels`
  does), append to that network's `Admins`, `config.SaveConfig`, rehash.
- DELETE: same `network` query param, same locate-and-mutate pattern.

**`web/templates/index.html`** — add a network `<select>` to the "IRC admin
hostmasks" card (`web/templates/index.html:725-746`), styled/wired like
`#irc-autojoin-network` (`index.html:796`, `ircAutojoinNetworkChange()`).
First option is `"(global — all networks)"` (empty value), followed by one
option per configured network. Update the card's explanatory `<p>` to
mention the two scopes.

**`web/templates/app.js`** — mirror the `selectedIRCNetwork` /
`ircAutojoinNetworkChange()` pattern (`app.js` ~2466-2532) for the admins
card:
- Add `selectedIRCConfigAdminNetwork` (default `''` = global).
- `fetchIRCConfigAdmins()`: append `?network=` when non-empty.
- `addIRCConfigAdmin()`: include `network: selectedIRCConfigAdminNetwork` in
  the POST body.
- `removeIRCConfigAdmin(i)`: include the same `network` param on the DELETE
  request.
- Populate the new `<select>` from the `lastIRCNetworks` array that
  `fetchIRCNetworks()` already fetches (no new network-list fetch needed) —
  hook into the existing `ircNetworksRender()` so the admins-card selector
  gets populated alongside `#irc-autojoin-network`.
- `onchange` handler updates `selectedIRCConfigAdminNetwork` and re-calls
  `fetchIRCConfigAdmins()`.

The ignore list has no web UI today (`!ignore` is IRC-only, in-memory,
non-persisted) — no dashboard work needed there.

## Tests

Add one focused test file `irc/permissions_test.go` (following the
`newTestBot` helper convention already used in `irc/cfg_race_test.go` /
`irc/social_test.go`) covering the security-relevant branch logic added:

- Global-list hostmask is admin on every network.
- Network-only-list hostmask is admin on its own network but not on a second
  configured network.
- `!ignore`'d nick is silenced on the network it was ignored on but not on a
  different network (construct two `ircNetwork`s sharing one `*Bot`, exercise
  the ignore map keying directly or via `handleCommand`).

## Verification

- `go build .` and `go vet ./...`.
- `go test ./irc/... -race` (covers the new test plus the existing
  `cfg_race_test.go` concurrency check against the modified `IsAdmin` path).
- Manual: run with two networks configured, one with a network-only
  `admins:` entry; confirm `!admin` login succeeds only on that network for
  that hostmask, and that a global-list admin can still log in on both.
  Confirm `!ignore` on one network doesn't silence the same nick on the
  other. Exercise the dashboard's new network selector on the admin-hostmask
  card (add/remove both global and network-scoped entries, confirm
  `config.yaml` reflects the right scope and a rehash applies it live).
