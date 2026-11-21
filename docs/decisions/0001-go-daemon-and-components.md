# 0001: Go daemon and component split

Status: accepted, 2026-11-19 (spec §4.1); confirmed by the stage 0c prototypes.

## Context

Zatvor must keep working when any single part fails: a UI crash, an Xray panic or a daemon restart must not
leave the user without internet or leave protected apps frozen forever. It also must run without admin rights
in the first stages and gain a narrow privileged part only in stage 3 (spec §4.1, §16 Q2.9).

## Options

- **One process** with UI, logic and protocol engine. Simple, but any crash takes everything down.
- **Go daemon plus separate processes** for UI, helper and supervisor (chosen).
- **Rust or C# core.** No advantage for this team; Go has the Xray ecosystem, easy cross-compilation and a
  small static binary.

## Decision

- **zatvord (Go)** is the brain: profiles, subscriptions, config generation, Xray lifecycle, protection
  engine, probes, network events, system proxy and dev-tool integration, journal and restore, local API
  ([0009](0009-local-api.md)). It runs as the user. Pure logic packages (`parse`, `gen`, `rules`, `guard`,
  `journal`) make no OS calls and take clock, network and filesystem interfaces, so they run on fake
  clocks in tests.
- **xray** is a supervised child process ([0002](0002-xray-separate-process.md)).
- **zatvor-supervisor** is the same binary in supervisor mode. It restarts zatvord with backoff and, if it
  gives up, releases or closes frozen apps by policy. It never holds system state.
- **zatvor-helper** (stage 3 only) is a narrow privileged API: TUN, routes, DNS, per-app network blocks
  (WFP/pf/nft). It does not touch the system proxy.
- **zatvor-ui** (tray, popup, panel, [0005](0005-ui-framework.md)) and the **zatvor CLI** are thin clients
  of the local API. A UI crash never affects protection.
- All system changes go through interfaces and a journal, and safe mode records instead of acting
  ([0004](0004-safe-mode-and-test-guard.md)).

## Consequences

- More processes to package, sign and update ([0006](0006-installer-and-install-script.md)).
- Every cross-process call goes through the local API, which has to be versioned and secured.
- Process-level isolation is what lets the daemon restore state after a crash of any other part.
- Prototypes in 0c did not change this split; stage 1 builds the daemon, CLI and UI on it.
