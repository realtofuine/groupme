# Revival notes (2026-09)

This branch (`revival-2026`) ports `mautrix-groupme` from the legacy
`maunium.net/go/mautrix/bridge` framework (last touched March 2023, pinned to
`mautrix v0.15.0`) to the current **bridgev2** framework in
`maunium.net/go/mautrix` (v0.31.0, September 2026). The legacy `bridge`
package no longer exists upstream, so this was a mandatory rewrite of the
bridge's Matrix-facing plumbing, not just a dependency bump. GroupMe-specific
business logic (message/attachment mapping, group vs. DM handling, likes)
was ported from the old `portal.go`/`user.go`/`puppet.go` rather than
rewritten from scratch, using `github.com/mautrix/gmessages` (an actively
maintained bridgev2 bridge from the same author/org) as the architectural
reference.

## Current status: compiles, builds, starts cleanly. Login + real-account deploy tried once; NOT fully integration-tested.

- `go build -tags goolm ./...` succeeds (pure-Go crypto, no libolm needed;
  useful for quick local iteration without `libolm-dev` installed).
- `go build ./...` (no tags) also works **inside the Docker image**, which
  installs `olm-dev`/`olm` via apk and links against real libolm — this is
  the build used for the shipped Docker image and is the recommended one for
  production (real libolm is better tested than the pure-Go fallback).
- `docker build -t groupme-bridge:revival2 .` succeeds using
  `golang:1.27-alpine3.23` as the build image (re-verified after the
  initial-sync and Faye HTTP/1.1 changes below).
- A live deploy against a real GroupMe account and Synapse homeserver
  confirmed login works end-to-end (access-token validation succeeds, bridge
  logs in as the real user), but **zero portal rooms were created**
  afterward. Root cause and fix: see "Initial chat sync" and "Faye/Bayeux
  push connection reliability" below. Neither fix has been verified against
  that live setup yet.
- The binary starts, connects to a local SQLite DB, runs bridge + Matrix
  state migrations successfully, generates an example config (`-e`) and an
  appservice registration (`-g`), and — when pointed at a fake/unreachable
  homeserver — retries the connection with backoff instead of crashing. No
  panics were observed in any of this. This was all verified by hand in this
  session; see the commands in the git log / PR description for exact repro
  steps.
- **Not verified**: an actual GroupMe account, an actual Matrix homeserver,
  or any live message flow. No GroupMe credentials or homeserver were
  available in this environment. Everything below "what's ported" should be
  treated as "compiles and follows the documented interface contracts, but
  has not exchanged a single real message."

## What's ported and should work in principle

- **Login**: a new `LoginFlowIDToken` flow (`pkg/connector/login.go`) asks
  for a GroupMe access token (same token model as before — GroupMe's REST
  API is still simple access-token auth, see below), validates it with
  `MyUser`, and stores it as bridgev2 `UserLogin` metadata.
- **Connect**: `pkg/connector/client.go` wires the existing
  `pkg/groupmeext` GroupMe API client + Faye push-subscription client
  (`github.com/karmanyaahm/wray`) into `NetworkAPI.Connect`/`Disconnect`.
- **Initial chat sync** (`pkg/connector/sync.go`, called from `Connect` in
  `client.go`): on every `Connect`, `GMClient.syncChats` lists the user's
  GroupMe groups (`IndexAllGroups`) and DM chats (`IndexAllChats`) via the
  REST API and queues a `ChatResync` (with `CreatePortal: true`) for each
  one, reusing the same `GetChatInfo`-based resync helper as the live
  push-triggered resyncs in `handlegroupme.go`. This used to be entirely
  missing: portals were only ever created reactively from live push events,
  so a freshly logged-in user got **zero** portal rooms until something
  happened to trigger a push for each chat. Runs in a background goroutine
  (detached from `Connect`'s context via `context.WithoutCancel`) so it
  doesn't block callers that invoke `Connect` synchronously (e.g. the
  unknown-error reconnect path in bridgev2 core). For DM chats this uses
  `Chat.OtherUser.ID` directly from `IndexChats`, which sidesteps the
  DM-portal-key heuristic problem described below for this code path
  specifically — the live push heuristic in `portalKeyForMessage` is
  unchanged and still has the caveat noted below.
- **Incoming messages** (`pkg/connector/handlegroupme.go`): GroupMe push
  events (`HandlerAll` from groupme-lib) are converted to bridgev2
  `simplevent` remote events — text messages, image attachments, likes
  (as a full reaction resync, since GroupMe's push payload doesn't say who
  added/removed a like, only the resulting list), and group name/topic/
  avatar/membership changes (all trigger a full `ChatResync` via
  `GetChatInfo`, matching the old bridge's "just resync everything" approach
  rather than incremental updates).
- **Outgoing messages** (`pkg/connector/handlematrix.go`): plain text only
  (matching the *previous* bridge's scope — outgoing media was never
  implemented pre-bridgev2 either), plus reactions (Matrix reaction <->
  GroupMe like, via `CreateLike`/`DestroyLike`).
- **Chat/user info** (`pkg/connector/chatinfo.go`): group name/topic/avatar/
  member list, DM peer name/avatar, ghost profile sync.

## What's NOT ported (known gaps)

- **Outgoing media** (images/files/locations from Matrix to GroupMe) — never
  existed in the old bridge either, so this isn't a regression, just an
  opportunity.
- **Incoming video/file/location attachments** — the old bridge's
  `handleAttachment` supported these (`portal.go` lines ~1008-1121 on
  `master`); only `image` attachments were ported to
  `convertGroupMeMessage` in `handlegroupme.go`. The logic to port is still
  visible in git history (`git show master:portal.go`).
- **Custom (double) puppeting via shared secret** — `custompuppet.go` and
  `tryAutomaticDoublePuppeting` in the old `user.go` were not ported.
  bridgev2 has its own double-puppeting support baked into the framework
  (`bridge.login_shared_secret_map` etc. in the generated config); it needs
  to be wired up/tested but wasn't in this session.
- **Provisioning API** (`provisioning.go`) — not ported. bridgev2 has a
  standard provisioning API surface; GroupMe-specific bits (if any were
  needed beyond login) would need porting.
- **Metrics / Segment analytics** (`metrics.go`, `segment.go`) — not
  ported. Low priority for a personal bridge.
- **Backfill** — not implemented (`BackfillingNetworkAPI` not implemented
  on `GMClient`). New portals will only show new messages after creation,
  not history.
- **Space rooms** (`GetSpaceRoom` in old `user.go`) — bridgev2's
  `personal_filtering_spaces` config option should cover this generically,
  but it hasn't been tested against this connector specifically.
- **Portal-key heuristic for DMs**: `GMClient.portalKeyForMessage` in
  `client.go` infers the DM "other user" as
  `msg.UserID != me ? msg.UserID : msg.RecipientID`. This mirrors the old
  bridge's general approach but wasn't validated against real GroupMe push
  payloads — GroupMe's DM `conversation_id` is actually a compound
  `"<id1>+<id2>"` string, and the old bridge had a `ParsePortalKey` helper
  to split it (see `database/portal.go` on `master`) that this heuristic
  does not replicate. **This is the single riskiest unverified piece** —
  it should be checked against real push payloads before relying on DM
  bridging.

## Dependency status

- `maunium.net/go/mautrix`: bumped `v0.15.0` -> `v0.31.0` (current as of
  2026-09-16). Go toolchain requirement bumped `go 1.19` -> `go 1.26.0`
  (toolchain `go1.27.1`, matching upstream's own `go.mod`). Go 1.27.1 was
  installed manually on this machine from https://go.dev/dl since apt's
  default was older.
- `github.com/beeper/groupme-lib`: **unchanged**
  (`v0.2.1-0.20221021205945-8f23e04eea71`). Checked upstream
  (github.com/beeper/groupme-lib) — the repo is **archived** and this pinned
  commit is already its last commit, so there is nothing newer to move to.
  GroupMe's REST API itself (dev.groupme.com) is still access-token based
  and hasn't publicly changed in a way that broke this client as far as
  could be determined without live credentials to test against.
- `github.com/karmanyaahm/wray` (Faye/Bayeux client for GroupMe's real-time
  push): **locally patched**, see below. Not archived, but last pushed
  2022-04-16. It's a small, self-contained implementation of the Bayeux
  protocol over HTTP long-polling and doesn't depend on mautrix-go, so the
  version bump didn't require touching it beyond adapting its logger
  interface (`pkg/groupmeext/subscription.go`) from the now-removed
  `maulogger/v2` to `zerolog` (which bridgev2 uses everywhere).

## Faye/Bayeux push connection reliability (`push.groupme.com/faye`)

A live deployment against a real GroupMe account observed the Faye handshake
to `push.groupme.com/faye` reliably failing with `504 Gateway Timeout` /
connection timeouts, both from this bridge's Faye client (`wray`) and from a
plain `curl` run on the same host against the same endpoint. The `curl` used
HTTP/2 by default (curl's current default for https URLs) and got no
response at all; the suspicion going in was that GroupMe's push
infrastructure (or whatever fronts it) does not handle this POST-based
long-polling transport correctly over HTTP/2.

Investigation confirmed a concrete, fixable bug on our side that matches
this symptom: `wray`'s `HTTPTransport.send` (the function issuing every
Bayeux long-poll request) used `http.Post`, i.e. Go's
`http.DefaultClient`/`http.DefaultTransport`. Go's default transport
auto-negotiates HTTP/2 over TLS via ALPN whenever the server advertises
`h2` — so this client would have been making the exact same kind of HTTP/2
request that reproduced the hang via `curl`.

**Fix applied**: `wray` is now vendored locally at `thirdparty/wray/` (a copy
of the pinned upstream version, `v0.0.0-20210303233435-756d58657c14`, with
one functional change) and pulled in via a `go.mod` `replace` directive
(`replace github.com/karmanyaahm/wray => ./thirdparty/wray`). The patch in
`thirdparty/wray/http_transport.go` sends requests through a dedicated
`http.Client` whose `Transport.TLSNextProto` is set to a non-nil empty map,
which disables `net/http`'s automatic HTTP/2 upgrade and forces HTTP/1.1.
See `thirdparty/wray/README.md` for the full rationale, and why a local
`replace` was used instead of a real upstream fork (no ability to publish a
fork from this sandboxed environment — swap in a real fork later if one
exists).

**This fix has not been verified against the live push server** — the
sandboxed environment that wrote it has no live GroupMe credentials or
network path to `push.groupme.com`. It's a plausible, mechanistically sound
fix for the exact symptom observed (Go's automatic HTTP/2 upgrade matching
the `curl --http2` reproduction), but it's possible the push service was
simply down/degraded for an unrelated reason at the time of testing, in
which case this patch won't change anything. **Next deploy should check
whether the Faye handshake now succeeds** before assuming this is fully
resolved; if it still fails identically, the push service itself is the
more likely explanation and this patch (and `thirdparty/wray/`) can be
reverted.

## Repository layout changes

- `main.go`, `user.go`, `portal.go`, `puppet.go`, `matrix.go`, `commands.go`,
  `custompuppet.go`, `provisioning.go`, `metrics.go`, `segment.go`,
  `messagetracking.go`, `bridgestate.go`, `formatting.go`, `database/`,
  `config/` — all removed. These implemented the legacy `bridge` package's
  interfaces (`bridge.Bridge`, a hand-rolled SQL `database` package, a
  hand-rolled `config` package built on `bridgeconfig.BaseConfig`), none of
  which exist in bridgev2. Their logic is preserved in git history on
  `master`/`upstream/master` for reference while porting the remaining gaps
  above.
- `groupmeext/` moved to `pkg/groupmeext/` (only the logger interface
  changed, from `maulogger/v2` to `zerolog`).
- New: `pkg/connector/` (the bridgev2 `NetworkConnector` implementation —
  this replaces `main.go` + `user.go` + `portal.go` + `puppet.go` +
  `config/`), `cmd/mautrix-groupme/main.go` (replaces old `main.go`,
  now a thin wrapper around `mxmain.BridgeMain`).
- bridgev2 owns its own database schema/migrations (Postgres or SQLite);
  the old hand-rolled `database/` package and its `upgrades/*.sql` are gone.
  Network-specific metadata (GroupMe access token, portal type) is stored
  via `GetDBMetaTypes()` in `pkg/connector/dbmeta.go` instead.

## Fallback plan

If further bridgev2 work stalls, the documented fallback (per the task that
produced this branch) is the older Node.js
[matrix-puppet-groupme](https://github.com/matrix-hacks/matrix-puppet-groupme)
bridge, which talks to GroupMe's stable access-token REST API directly and
doesn't need this level of framework rework, at the cost of a much
older/simpler architecture (no double puppeting, no bridgev2 features like
built-in provisioning/backfill/space rooms).

## Suggested next steps

1. Get real GroupMe credentials and a test Matrix homeserver, then actually
   exercise login, an incoming group message, an incoming DM, an outgoing
   message, and a like/reaction in both directions. In particular, confirm
   the initial chat sync (see above) actually creates a portal for every
   existing group/DM after login, and check whether the Faye HTTP/1.1 patch
   actually fixes the push handshake (see "Faye/Bayeux push connection
   reliability" above) — neither has been verified live yet.
2. Fix the DM portal-key heuristic (see above) once real push payloads are
   available to confirm the actual shape of `ConversationID`/`ChatID` for
   DMs vs. groups. Note the initial sync path (`pkg/connector/sync.go`)
   doesn't hit this problem since it gets `OtherUser.ID` directly from
   `IndexChats`; only the live push handler (`portalKeyForMessage` in
   `client.go`) still has the heuristic.
3. Port video/file/location attachment handling from `master`'s
   `portal.go` `handleAttachment` (git history has the full implementation).
4. Wire up double puppeting (`bridge.login_shared_secret_map`) and confirm
   it works with bridgev2's built-in support.
5. Consider implementing `BackfillingNetworkAPI` so newly created portals
   get recent history instead of starting empty.
6. If the Faye HTTP/1.1 patch (`thirdparty/wray/`) is confirmed to fix the
   push handshake, consider upstreaming it as a real fork/PR against
   `github.com/karmanyaahm/wray` instead of carrying a local `replace`
   indefinitely. If it *doesn't* fix the handshake, that's strong evidence
   the problem is external (GroupMe's push service itself), and the patch
   can be reverted.
