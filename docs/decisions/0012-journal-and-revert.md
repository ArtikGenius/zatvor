# 0012: Journal and revert semantics

Status: accepted, 2026-11-22 (spec §8; `internal/journal`, `internal/sysproxy`, `internal/supervisor`, `internal/daemon`).

## Context

Zatvor changes the user's system: the system proxy and, with dev-tool routing, shell profiles and tool
settings. A crash, a kill or a power loss must not leave those changes behind, and a revert must never
destroy something the user or another program changed after us.

## Decision

- **Write ahead (R8).** Before a change, `Begin(entry)` appends a JSON line with the target, the previous
  value (`Prev`), what we set (`Applied`) and the reverter `Kind`, and fsyncs it. It returns the entry ID
  (a signature change from the plan: callers need the ID). After the change is done and checked, `Done(id)`
  appends `{"done":id}`. Pending means begun and not done.
- **Revert only what is still ours (R9).** `RevertAll` walks pending entries newest first. It asks the
  `Reverter` of the entry's `Kind` for the target's `Current` value and compares it with `Applied` as
  canonical JSON (numbers kept as written).
  - Equal: restore `Prev` (through `RestoreIf` where the reverter can check again at write time), then close
    the entry: `Restored`.
  - Equal to `Prev` already: the crash hit before `Done`; close it: `Restored`.
  - Anything else: someone changed it after us. Keep their value, close the entry, report
    `SkippedChangedByOther`, and let the caller warn.
  - A missing reverter or a `Restore` error leaves the entry pending (`Failed`) and blocks older entries of
    the same kind and target (`ErrBlockedByNewer`), so an old value is never put back over a newer failed
    one. It is retried at every start.
  - The file is compacted to empty only when nothing is pending.
- **One process at a time (R10).** There is no file lock. The daemon holds the single-instance lock, and the
  supervisor calls `RevertAll` only after its child has exited. A test guards that order.
- **Two journals (R16).** Safe mode and live mode have separate journal directories (`Journal/safe`,
  `Journal/live`). A safe start never reverts live entries through the recorder, which would close them as
  changed by someone else.
- **Reverters are methods (R2).** `(*sysproxy.Manager).Reverter()` and `(*devtools.Manager).Reverter()`
  return `journal.Reverter` for the kinds `sysproxy` and `devtools`.
- **Torn tail.** A half-written last line (the process died mid-append) is cut off on open; an unparsable
  line in the middle is skipped.

## How it is used

- The daemon runs `RevertAll` at start (crash recovery) and at normal exit, after dev tools and the system
  proxy were turned off.
- The supervisor restarts a crashed daemon with a backoff (5 crashes in 2 minutes make it give up, R40).
  When it gives up, or when its context ends during a backoff after a crash, it calls `RevertAll` itself,
  after the child's `Wait` returned, so the system proxy is restored even when the daemon cannot come back
  (R40b). A clean exit 0 reverts nothing: the daemon already did.

## Consequences

- A stuck entry stays in the journal and is retried at every start; it never silently disappears.
- Without autostart, a killed process tree or a power loss leaves the system proxy on a dead port until
  the next launch of Zatvor (spec §8). Revisit with autostart in 1c.
- No cross-process locking: a second process using the same journal directory is not supported.
