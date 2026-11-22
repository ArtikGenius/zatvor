# 0009: Local API

Status: accepted, 2026-11-19 (spec §4.5). Implemented in stage 1b (`internal/api`, `internal/instance`);
transport settled 2026-11-22: HTTP only in 1b, named pipe reconsidered in 1c.

## Context

zatvor-ui, the CLI and later a browser extension are separate processes that need state and commands from
zatvord ([0001](0001-go-daemon-and-components.md)). Any local web page or other user process could try to
reach a localhost listener, so the API must resist DNS rebinding, cross-origin requests and other local
users.

## Options

- **Named pipe or Unix socket only.** Strong isolation, but browser extensions and WebView UIs cannot use
  it directly.
- **HTTP on 127.0.0.1 with a token** (chosen), with a named pipe for CLI/UI on Windows where the framework
  allows it.

## Decision

- HTTP on `127.0.0.1`, a random free port (fallback ports far apart), published in a discovery file.
- A token in a user-only file; every request carries it.
- `Host` must be `127.0.0.1:<port>`; `Origin` absent or our own (blocks rebinding and cross-origin pages).
- JSON bodies; long-poll or SSE for state changes.
- Used by the UI, the CLI and later the browser extension.
- Transport, settled for 1b: **HTTP only.** 1b has no UI, and the CLI uses the same HTTP
  API as everything else, so one transport and one set of checks is tested. A named pipe for the UI and
  CLI is reconsidered in 1c, when the UI is wired (the Tauri prototype in [0005](0005-ui-framework.md)
  did not exercise it).

## Implementation (stage 1b)

- **Listener.** `127.0.0.1` only (tcp4). The persisted port (`Settings.LocalAPIPort`, its own field:
  `Settings.Ports.API` is Xray's api inbound) is tried first, unless it is 0 or equals any of Xray's
  ports; `127.0.0.1:0` is the fallback, and the daemon persists the bound port. On Windows the socket has
  `SO_EXCLUSIVEADDRUSE`, so another socket cannot bind the port with `SO_REUSEADDR` and receive the
  clients' token.
- **Discovery.** `Run/token` (32 random bytes, 64 lowercase hex) is written first, then `Run/api.json`
  = `{"version":1,"port":N,"pid":P}`, so a reader that finds `api.json` always finds the token. Both
  are written atomically (temp file, fsync, rename) and restricted to the user before the rename (a
  protected single-ACE DACL on Windows, 0600 elsewhere). Stop removes `api.json` only if its pid and
  port are still ours and the token only if it is still ours (K34).
- **Checks on every request**, failing with 403 `{"code":"forbidden","message":"forbidden"}` and no
  detail: `Host` equals the exact string `127.0.0.1:<port>`; `Origin` is absent, empty or `null`
  (1c adds the UI's own origin); `Authorization: Bearer <token>` matches in constant time. A CORS
  preflight from a page therefore fails, and no CORS headers are ever sent.
- **Bodies** are capped at 64 KiB (413 `body_too_large`), must be one JSON value, and every response
  is JSON with `Cache-Control: no-store` and `nosniff`. The one exception is the zip of `GET diag`
  (`application/zip`), which is built in memory first, so a failure is still a JSON error.
- **Errors** are `{"code","message"}`. A domain error (`engine`, `subsvc`, `devtools`, `api.Error`,
  also when wrapped) gives its own code and message; the text of a wrapped error, which can hold a URL
  or a path, never reaches a body. Anything else is `{"code":"internal","message":"internal error"}`.
  Status: 400 bad input (`bad_request`, `invalid_url`, `settings_empty`), 403 `forbidden`, 404
  `not_found`, 409 conflicts (`proxy_conflict`, `no_conflict`, `superseded`, `devtools_conflict`,
  `devtools_changed_by_other`, `not_connected`), 413, 500 `internal`, and 422 for every other domain
  failure.
- **Routes v1:** `GET status`, `GET events`, `GET profiles`, `POST connect {profile}`,
  `POST disconnect`, `GET subscriptions`, `POST subscriptions {url, ua}`,
  `POST subscriptions/{id}/update`, `DELETE subscriptions/{id}`, `PUT settings`, `POST devtools {on}`,
  `POST proxy/resolve-conflict {force}`, `POST show`, `POST check` (a fresh tunnel check through
  the connected tunnel; the check result, or 409 `not_connected`), and `GET diag` (the redacted
  diagnostics zip).
  Listings carry no secrets: a subscription shows its URL's host only and a profile no address,
  credential or link.
- **Events.** `GET /v1/events` is SSE with the events `state`, `subscription` and `warning`; the
  current state is sent first. The daemon is the only reader of the engine's and subsvc's event
  channels and fans them out to the API.
- **Single instance.** Windows: the named mutex `Local\Zatvor-<user SID>`, held until exit; elsewhere
  `flock` on `Run/instance.lock`. A second launch POSTs `/v1/show` with the token (3 s timeout, no
  proxy) and exits (K43).

## Consequences

- The API is a security boundary: token handling, Host/Origin checks and file permissions get tests.
- The API must be versioned, because UI and daemon update separately.
- Single-instance "show window" requests also go through it (spec §4.6).
