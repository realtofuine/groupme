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
- **REST polling fallback** (`pkg/connector/poll.go`): a resilient
  fallback/replacement for incoming-message delivery via the REST API,
  running alongside the Faye push connection rather than instead of it —
  added because Faye has been observed failing persistently in production
  (see "Faye/Bayeux push connection reliability" and "REST polling
  fallback" below for the full writeup).

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

A live deployment after the HTTP/1.1 patch above confirmed the 504s persist
identically — the Faye handshake has now failed on every attempt for over an
hour of continuous 10s-backoff retries, both before and after that fix. That
rules out the HTTP/2-vs-1.1 theory as the (sole) cause and points at
GroupMe's push infrastructure itself being degraded or blocked for this
connection, not a client-side bug. **This is why the REST polling fallback
below exists**: with Faye down, incoming messages (and even the logged-in
user's own messages sent from the native GroupMe app) had no path into
Matrix at all, since push was the only mechanism that ever fed new messages
into the bridge.

A related bug was found and fixed in the same investigation:
`GMClient.Connect` (`pkg/connector/client.go`) used to call
`gc.conn.SubscribeToUser(...)` — wray's Bayeux handshake, which retries
internally with its own backoff and has no timeout — **synchronously**,
before kicking off the initial chat sync goroutine. Since that handshake
was observed blocking for over an hour straight, this meant a dead Faye
server silently prevented the initial sync (and now, REST polling) from
ever starting too, even though neither actually depends on Faye succeeding.
`SubscribeToUser` is now called in its own goroutine, and chat sync + REST
polling are started unconditionally right after `Connect` sets up the Faye
listener, instead of after the handshake resolves. Faye is now purely an
optional low-latency accelerator: incoming messages get bridged over
whichever path (push or poll) sees them first.

## REST polling fallback (`pkg/connector/poll.go`)

Since the Faye push connection has proven unreliable in production (see
above) and was, at the time of writing, the *only* mechanism through which
new messages ever reached Matrix, `pkg/connector/poll.go` adds REST-API
polling as a resilient fallback/replacement, running alongside Faye rather
than instead of it.

- **What it polls**: `github.com/beeper/groupme-lib` (the pinned REST
  client, unchanged/archived — see "Dependency status" above) exposes
  `Client.IndexMessages(ctx, groupID, *IndexMessagesQuery)` for group
  messages (supports `SinceID`/`AfterID`/`BeforeID`/`Limit`) and
  `Client.IndexDirectMessages(ctx, otherUserID, *IndexDirectMessagesQuery)`
  for DMs (supports `SinceID`/`BeforeID`, keyed by `other_user_id`). Both
  are called directly (not through the pre-existing but unused
  `groupmeext.Client.LoadMessagesAfter` helper, which does the same thing
  but internally uses `context.TODO()` instead of a caller-supplied
  context — polling needs real context cancellation so the loop stops
  promptly on `Disconnect`).
- **Which chats get polled**: every poll tick re-lists the user's groups
  and DM chats via `gc.Client.IndexAllGroups()` / `IndexAllChats()` — the
  exact same calls `syncChats` (`sync.go`) uses for the initial sync — so
  newly created chats are picked up automatically without maintaining a
  separate chat list.
- **Interval**: configurable via the new `poll.interval_seconds` config key
  (`pkg/connector/config.go`, default **20s**, clamped to a 10s floor
  regardless of config). 15-20s was chosen per the task guidance: GroupMe
  doesn't aggressively rate-limit lightly-polled read endpoints for a
  single personal account, but there's no reason to poll faster than that,
  and the 10s floor guards against a config typo turning this into a tight
  request loop. `poll.enabled` (default `true`) turns polling off entirely
  if it's ever no longer needed.
- **"Last seen message ID" tracking**: reuses bridgev2's own message store
  instead of a new table — `DB.Message.GetLastNInPortal(ctx, portalKey, 1)`
  already returns the most recently bridged message for a portal (inserted
  there by *either* Faye or a previous poll), and that naturally survives
  bridge restarts since it's just the existing message history. If a
  portal has no bridged messages yet (e.g. one just created by initial
  sync with nothing pushed to it since), there's no cursor to poll forward
  from, so GroupMe's list endpoints are called with no since/after filter,
  which returns their default page of the most recent messages. This isn't
  a real backfill implementation (see "Backfill" below), but it's a
  harmless, bounded side effect: newly-synced empty portals opportunistically
  get a page of recent history instead of staying silent until something
  new arrives.
- **Conversion path**: every new message fetched by polling is passed to
  `GMClient.HandleTextMessage` — the exact same method the Faye push
  handler calls for live messages (`handlegroupme.go`) — so there is no
  separate/duplicated message-to-bridgev2-event conversion logic. This
  also means the logged-in user's own messages (sent from the native
  GroupMe app) are bridged with no special-casing: GroupMe's message-list
  endpoints return the same message shape (`UserID`/`RecipientID`/
  `GroupID`) as push payloads, so `HandleTextMessage`'s existing
  `IsFromMe`/portal-routing logic just works.
- **Dedup**: bridgev2 core already dedupes incoming `RemoteEventMessage`s
  by message ID before doing anything observable
  (`Portal.handleRemoteMessage` → `DB.Message.GetAllPartsByID`, in
  `maunium.net/go/mautrix/bridgev2/portal.go`) — if the ID already has a
  bridged message, the event is silently ignored. Since both the Faye
  handler and the poller feed `networkid.MessageID`s derived the same way
  (`MakeMessageID(msg.ID)`, the raw GroupMe message ID) into the same
  event type, this dedup applies uniformly regardless of source: whichever
  of Faye/polling sees a given message first wins, and the other is a
  no-op. No polling-specific dedup logic was needed.
- **Lifecycle**: the poll loop is started in `GMClient.Connect` as its own
  goroutine with a cancellable context (`gc.pollCancel`), and stopped in
  `GMClient.Disconnect`; it does not depend on the Faye handshake
  succeeding or even being attempted (see the `Connect` restructuring
  above).

**Not verified live**: like the rest of this branch, this was written and
built (including a full Docker build with real libolm) without live
GroupMe credentials, so the actual REST calls, their response shapes, and
real-world rate-limit behavior have not been exercised against
`api.groupme.com`. On next deploy, check that: portals that were empty
after initial sync pick up a page of recent messages within one poll
interval; a message sent from another client into an existing chat shows
up in Matrix within ~20s even with Faye still down; and a message sent
from the native GroupMe app by the bridge's own account also shows up
(this was the original trigger for this work). If Faye recovers at some
point, also confirm a message it delivers doesn't show up twice.

With this change, "no reliable message delivery when Faye is down" (the
problem that motivated this work) should be addressed: message delivery no
longer depends on Faye succeeding at all. What's *not* addressed is
real-time latency when Faye is down — polling caps latency at roughly one
poll interval (~20s) instead of push's sub-second delivery — but the task
explicitly treats that as an acceptable tradeoff for a resilient fallback,
not a regression to fix.

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
   reliability" above) — neither has been verified live yet. Also confirm
   the REST polling fallback (`pkg/connector/poll.go`, see "REST polling
   fallback" above) actually delivers new messages — including the bridge's
   own account's messages sent from the native app — within one poll
   interval, and that an empty portal picks up a page of recent history on
   its first poll.
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
   get recent history instead of starting empty. The REST polling fallback
   (`pkg/connector/poll.go`) opportunistically delivers one page (~20) of
   recent messages the first time it polls a portal with no bridged
   history yet, as a side effect of how it seeds its "since" cursor — see
   "REST polling fallback" above — but that's incidental, not a real
   backfill implementation (no pagination past one page, no user-facing
   config, no distinction from a live message for e.g. notification
   purposes).
6. If the Faye HTTP/1.1 patch (`thirdparty/wray/`) is confirmed to fix the
   push handshake, consider upstreaming it as a real fork/PR against
   `github.com/karmanyaahm/wray` instead of carrying a local `replace`
   indefinitely. If it *doesn't* fix the handshake, that's strong evidence
   the problem is external (GroupMe's push service itself), and the patch
   can be reverted.
