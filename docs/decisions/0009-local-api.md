# 0009: Local API

Status: accepted, 2026-11-19 (spec §4.5). Implementation is stage 1.

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
- On Windows, prefer a named pipe for CLI and UI when the framework supports it; to be settled when the
  UI is wired in stage 1 (the Tauri prototype in [0005](0005-ui-framework.md) did not exercise it).

## Consequences

- The API is a security boundary: token handling, Host/Origin checks and file permissions get tests.
- The API must be versioned, because UI and daemon update separately.
- Single-instance "show window" requests also go through it (spec §4.6).
