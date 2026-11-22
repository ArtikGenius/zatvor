# Status

Stage 0c (prototypes) is complete as of 2026-11-21, pending the merge to main and the macOS UI check. Next: stage 1. Plan of 0c:
[docs/superpowers/plans/2026-11-19-stage-0c-prototypes.md](superpowers/plans/2026-11-19-stage-0c-prototypes.md).
What to check: [docs/checklists/stage-0c.md](checklists/stage-0c.md).

## Done in 0c

- Repository foundation: licence, secret scan, CI.
- Subscription fixtures and golden tests; geo tag scanner and synthetic geo files.
- Verified Xray fetch (`internal/xraybin`), real-binary integration tests on Linux and Windows.
- Legacy noise migration with refusals for ambiguous cases.
- Safe mode, system recorder and test process guard; freeze-safe system tests on Windows CI.
- Prototypes measured on GitHub Actions: TUN (A vs B), UI (Tauri vs Wails), installer (WiX vs Inno).

## Decisions

See [docs/decisions/README.md](decisions/README.md).

- [0001](decisions/0001-go-daemon-and-components.md) Go daemon and components
- [0002](decisions/0002-xray-separate-process.md) Xray as separate process
- [0003](decisions/0003-tun-front.md) TUN: Xray `tun` inbound
- [0004](decisions/0004-safe-mode-and-test-guard.md) Safe mode and test guard
- [0005](decisions/0005-ui-framework.md) UI: Tauri 2 (provisional until the Mac check)
- [0006](decisions/0006-installer-and-install-script.md) Installer: WiX v5 / MSI
- [0007](decisions/0007-xray-pin-and-geo-merge.md) Xray pin and geo merge
- [0008](decisions/0008-hwid-algorithm.md) HWID, frozen forever
- [0009](decisions/0009-local-api.md) Local API

## Open follow-ups

Platform checks (stage 1 decision: automated Windows checks run on GitHub Actions Windows runners; hands-on checks run on Artur's Windows PC through a remote session when he is available, in safe mode except for the acceptance run he approves):
- Mac tray check pending with Artur (`proto/ui/RESULTS.md`).
- Windows Sandbox harness deferred (Artur cannot reboot now; CI Windows runners used instead).
- Windows-taskbar tray placement check deferred to stage 1 (no Windows device connected).
- IPv6 leak and sleep/wake for TUN moved to the stage 3 VM.

Design constraints from ADRs:
- Xray process-path rules must canonicalise path case (ADR 0003).
- Fail-closed across an Xray crash needs helper-owned persistent WFP filters (ADR 0003).
- Check the Wintun prebuilt-binary licence before the first release (ADR 0003).
- Release-build default stays safe until releases ship (ADR 0004).

Deferred minor review findings:
- secretscan: `allow.txt` allows the whole placeholder host `panel.real-host.ru` (use example.net); `skipDirs` applies at any depth; private-key doc comment says 44 chars, regex 43+.
- geo/synth: test does not load via Xray's loader (covered by the integration test); `synth.Write` does not dedup input tags.
- xraybin: no zip-slip regression test; `MkdirAll` before validation; `LimitReader` truncates silently.
- noise migration: untagged outbound reported as an empty tag; dead int case in `encodeValue`; hex internal-whitespace comment.
- xrayint: `freeTCPPort` TOCTOU; `ds[1]` shape not compared across runs; no early-exit detection; broad "deprecated" substring.
- testguard/sysapi: registry is per process, not per test. (Check-to-action TOCTOU: resolved in Task 7, freeze acts on the handle it checked.)
- systest: cleanup `Resume` skips Check and ignores its error; gate convention not enforced for future packages (add a CI grep); invalid-pid error lacks context.
- proto/tun: NOTICE lacks transitive deps of `proto/tun/b`.
- installer scripts: `install.ps1` hash-to-run TOCTOU (hold the handle); cofactored PowerShell vs cofactorless OpenSSL verification policy note; two untested rejection branches; `install.sh` trap for INT/TERM; exit code for a missing file.

## Next: stage 1

Windows VPN client: import all formats, geo, system proxy, routing, basic tray and panel, subscription
updates, crash recovery and rollback, diagnostics, CLI `status` and `check`.
Acceptance: Artur's test subscription imports; Hysteria2-DNS, XHTTP and Vision connect; Artur's
home-internet check.
Open for planning: Artur has no interactive Windows device, so stage 1 must say where Windows checks run
(CI runner, cloud Windows VM, or moved to the Mac). This affects the acceptance above, the tray placement
check (ADR 0005) and the Hyper-V check (ADR 0003).
