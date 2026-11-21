# 0002: Xray runs as a separate process

Status: accepted, 2026-11-19 (spec §4.2); the 0c integration tests ran the real binary as a child.

## Context

Zatvor uses the official Xray-core as its only protocol engine. The daemon holds TUN, routes, WFP filters
and the freeze journal. Xray panicked several times in 2026 (upstream issues #7369, #6729, #6653); see
`docs/research/xray-core.md`.

## Options

- **Library mode** (link Xray or libXray into zatvord). No IPC, but a panic kills the daemon and its
  state. libXray "does not guarantee API stability" (research).
- **Separate official binary** supervised by zatvord (chosen).

## Decision

Xray is the official signed binary run as a child of zatvord. It is pinned and verified against the release
digest ([0007](0007-xray-pin-and-geo-merge.md)). The daemon talks to it over Xray's gRPC API (stats,
outbound hot-swap), validates every generated config with `xray -test` before use, and restarts it with
backoff after a crash. With the TUN inbound ([0003](0003-tun-front.md)) the daemon also owns what must
survive an Xray crash.

## Consequences

- An Xray panic costs a restart (measured in 0c: TUN up again ~0.3 s after the new process starts), not
  the daemon.
- We ship and update a second binary, and carry its licence (MPL-2.0) in `NOTICE`.
- gRPC IPC is needed for stats and hot-swap; acceptable cost.
- Fail-closed across an Xray crash cannot rely on Xray's own filters; see 0003.
