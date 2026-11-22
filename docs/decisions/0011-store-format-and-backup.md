# 0011: Encrypted store format and backup

Status: accepted, 2026-11-22 (spec R3.5; `internal/store`).

## Context

Subscription URLs and credentials are secrets. They must not reach disk in clear, and a crash or a bad
update must not lose good data.

## Decision

- One file: `ZTVS`, a version byte, a 12-byte random nonce, then the AES-256-GCM ciphertext of a JSON
  document. Header and nonce are authenticated as additional data. The key is 32 bytes from a key
  provider (OS-backed providers come in 1b; a 0600 key file exists for tests and Linux).
- Save writes a temp file in the same directory, fsyncs it, and renames it over the main file. The main
  file is present at every instant.
- Before that, the current main file is copied to `.bak` only if it decrypts with the current key. A bad
  main file never replaces a good backup.
- Load never writes and never falls back to `.bak`. Callers Load before Save, so a wrong key or damaged
  file is noticed before it is overwritten.
- Update merging keeps old data when a new fetch is bad: a failed routing header decode keeps the old
  provider routing. An absent or disabled header clears it.
- `ojson.Object` serialises with key order kept, so stored configs round-trip byte-stable.

## Consequences

- One generation of backup. Restore from `.bak` is a 1b decision (revert on start).
- Two Store instances on one directory are not supported.
