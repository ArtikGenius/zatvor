# 0004: Safe mode and test process guard

Status: accepted, 2026-11-21 (stage 0c, Task 6)

## Context

Zatvor freezes and closes other apps and changes the system proxy. The developer's own PC runs Claude, Claude Code (node.exe) and another VPN; a built Zatvor binary or a test must never change that host, and freezing code must never touch a process a test did not start. PIDs are reused, so a PID alone does not identify a process.

## Decision

- **Safe mode** (`internal/safemode`). `Resolve(args, env)` picks `Safe` or `Live`. Precedence: flag `--safe`/`--live`, then env `ZATVOR_SAFE` (`1` safe, `0` live, any other value safe), then the build default. `--safe` with `--live` is safe. Flags are recognised in every form the Go flag package accepts (`-safe`, `--safe=true`, `-live=1`, ...); a value that is not a bool means safe, and `--safe=false`/`--live=false` request nothing. All builds, dev and `release` alike, default to safe, and any live request is refused (result: safe, source "live refused") unless `ZATVOR_ALLOW_LIVE=1` is set exactly; only CI jobs set it (systemtest-windows from Task 7, proto-tun from Task 8). Revisit the release-build default when release builds exist. `zatvord` in 0c knows only `--version`, `--safe`, `--print-mode` (`--live` is not even a flag there).
- **Recorder** (`internal/sysapi`). All system changes go through interfaces (`SystemProxy`, `ProcessControl`, keyed by `ProcID{PID, Created}`). `Recorder` implements them by appending one JSON line `{"ts","op","args"}` per call and changing nothing; it is what safe mode uses.
- **Test guard** (`internal/proc/testguard`). Tests act only on processes from `Spawn`, which builds and starts the helper `testguard/child` with a random `ZATVOR_TEST_TOKEN` and a private per-spawn directory (`t.TempDir()`) in `ZATVOR_GUARD_DIR`. `Check(id)` accepts only if: the PID is in this test process's spawn registry; the process runs (zombies count as exited); its OS creation time equals `id.Created` exactly; its parent is the test process; and the file `handshake` in the directory recorded for it (written by the child via `os.CreateTemp` + rename) holds the token. Nothing is written to the shared temp dir under a predictable name, so there is no symlink-plant risk and nothing leaks on a crash beyond the test temp dir. Every error is a refusal. Creation time and parent come from `GetProcessTimes` + `NtQueryInformationProcess` (Windows), `/proc/<pid>/stat` fields 4 and 22 with `AT_CLKTCK` from the auxiliary vector and `btime` read once per process (Linux), `sysctl kern.proc.pid` (macOS). Another process's environment is never read on any OS.
- The child is built with `go build` into the test temp dir rather than re-executing the test binary, because a package that forgot the re-exec hook would run its whole test suite recursively as the "child". The child exits when its stdin closes (the parent died) and after 10 minutes at most.

## Consequences

- A binary built on the host is inert by default; going live needs two deliberate switches in every build.
- Freezing code (Task 7 on) calls `testguard.Check` before acting in tests; PID reuse, foreign PIDs and exited children are refused (pinned by `TestGuardRefuses*`).
- Tests that spawn need the `go` tool on PATH (true in CI and in dev shells) and pay one cached build per test.
- On Linux the creation time is only as exact as `btime` (whole seconds) and USER_HZ ticks; it is stable within one process, which is all the guard compares. Stage 1 will add handles (Windows) / pidfd (Linux) for long-lived identity (known-pitfalls K2).
- `golang.org/x/sys` (BSD-3-Clause) becomes a direct dependency; listed in `NOTICE`.
