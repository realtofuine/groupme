# Vendored, patched `github.com/karmanyaahm/wray`

This is a local copy of the non-test source files from
[`github.com/karmanyaahm/wray`](https://github.com/karmanyaahm/wray)
`v0.0.0-20210303233435-756d58657c14` (the version pinned by
`github.com/beeper/groupme-lib`, and previously imported directly by this
repo), with one deliberate patch in `http_transport.go`.

## Why this exists

`HTTPTransport.send` (the function that performs each Bayeux long-polling
request against GroupMe's real-time push server,
`https://push.groupme.com/faye`) used `http.Post`, i.e.
`http.DefaultClient`/`http.DefaultTransport`. Go's default transport
auto-negotiates HTTP/2 over TLS via ALPN whenever the server advertises
`h2`.

While deploying the bridgev2 port of this bridge against a real account, the
Faye handshake to `push.groupme.com/faye` reliably failed with `504 Gateway
Timeout` / connection timeouts — both from this Go client and from a plain
`curl` run on the same host with HTTP/2 (curl's current default). The
timeouts did not reproduce with HTTP/1.1. This strongly suggests whatever is
in front of `push.groupme.com/faye` does not handle this POST-based
long-polling transport correctly over HTTP/2 (some Bayeux/long-poll
proxies/back ends don't).

`http_transport.go` here now sends requests through a dedicated
`http.Client` whose transport has `TLSNextProto` set to a non-nil empty map,
which tells `net/http` not to auto-upgrade to HTTP/2, forcing HTTP/1.1. This
is the only functional change from upstream. Everything else in this
directory (`wray.go`, `response.go`, `schedular.go`, `transport.go`,
`utils.go`) is an unmodified copy, kept only because Go module `replace`
directives operate on whole modules/packages, not individual files.

## Why a local `replace` instead of a fork

Publishing a real fork (`github.com/<org>/wray`) and updating the `go.mod`
`require` line would be the normal fix, but this patch was written in a
sandboxed environment with no ability to push to a new GitHub repo. A local
`replace github.com/karmanyaahm/wray => ./thirdparty/wray` in the top-level
`go.mod` was used instead so the fix ships in this repo without depending on
an external fork existing. If/when a real fork (or an upstream fix) exists,
this directory and the `replace` line in `go.mod` can be deleted and the
normal pinned dependency restored.

## Caveat

This fix has **not** been verified against a live `push.groupme.com/faye`
endpoint (no live GroupMe credentials in the environment that wrote this
patch) — it's based on a plausible, concrete mechanism (Go's automatic HTTP/2
upgrade) matching an external symptom (a raw HTTP/2 curl to the same host
also got no response). If the push server is simply down/degraded
independent of protocol version, this patch won't help; see `NOTES.md` at
the repo root for the current status of that investigation.
