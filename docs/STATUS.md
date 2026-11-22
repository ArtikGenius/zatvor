# Status

Stage 1a (OS-independent core engine) is complete as of 2026-11-22 on branch `stage-1`, pending review and the merge to main. Next: stage 1b (zatvord on Windows). Stage 0c is complete, pending the macOS UI check.
Plan of 1a: [docs/superpowers/plans/2026-11-22-stage-1a-core-engine.md](superpowers/plans/2026-11-22-stage-1a-core-engine.md).
What to check: [docs/checklists/stage-1a.md](checklists/stage-1a.md) (nothing to run by hand).
Plan of 0c: [docs/superpowers/plans/2026-11-19-stage-0c-prototypes.md](superpowers/plans/2026-11-19-stage-0c-prototypes.md); checklist [docs/checklists/stage-0c.md](checklists/stage-0c.md).

## Done in 1a

- `ojson`: JSON with key order kept.
- `link`: share-link parser and exporter, outbound mapping checked against Xray's own builder.
- `subbody`, `subhdr`, `hwid`: tolerant subscription body decoder, header parsing, frozen HWID.
- `happ`: Happ routing profile and FakeDNS translation.
- `subfetch`: single-flight subscription fetcher with redirect policy and error scrubbing.
- `profile`, `store`: update merge and diff, encrypted store with backup.
- `gen`: config builder with rule layers and presets; provider routing kept intact.
- `geo/merge`: merged geo files per tag set.
- `validate`: schema check and `xray -test`, secrets scrubbed from messages.
- `xrayrun`: supervised Xray runner and core latency test.
- `e2e`: three real-Xray end-to-end tests plus a legacy-config test and an awkward-secret test.
- Final review fix wave (2026-11-22): no `Referer` on redirects; provider `metrics` removed; typed errors in `link`, `store`, `validate` and `xrayrun`; negated geo tags; TCP HTTP header refused; bad JSON array elements skipped; generation-checked `Stop`; complete key file on first use; store document v2 (provider Happ profile stored once). Report: `.superpowers/sdd/2026-11-22-stage-1a-core-engine/final-fix-report.md`.

CI evidence: run [45016047924](https://github.com/ArtikGenius/zatvor/actions/runs/45016047924) (commit 8d079b3, first all-green with the e2e tests) and run [45016399057](https://github.com/ArtikGenius/zatvor/actions/runs/45016399057) (commit 8528d14, latest at the time of writing).

Known CI incident: the `secrets` CI job failed from 1f7ccd3 until eec06c6. The pre-commit hook was not enabled in the working clone, so fake test UUIDs reached CI unchecked. Fixed by allow-listing the fake UUIDs and adding `scripts/dev/enable-hooks.sh`. Run it once in every new clone.

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
- [0010](decisions/0010-provider-routing-and-proxy-tag.md) Provider routing never rewritten; ProxyTag rule
- [0011](decisions/0011-store-format-and-backup.md) Encrypted store format and backup

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

Deferred minor review findings from 1a:
- link: `security=none` to a public host builds but Xray refuses at start; legacy base64 host unwrapping is duplicated; hy2 multi-port uses the first port without a note; pin list keeps empty segments and does no hex check.
- ojson: doc comments and method order.
- subbody: Clash bodies carry junk skipped entries; `#http://x` is read as an inline header; a few cases lack tests (headers-only body, base64 inline headers).
- subhdr, hwid: fractional update interval accepted; `x-hwid-limit` merged into the limit-reached flag; missing tests for bad base64 title and over-256 cap.
- happ: FakeDNS on with no proxy sites puts a catch-all fake server first (untested); small type-switch duplication; DNS default IPs are literals.
- subfetch: no tests for a custom dialer, ignored env proxy, encrypted body or corrupt gzip; the 20 s default is not a named constant; the "failed" error drops the cause category (refused, DNS, TLS); some tests order by sleeps.
- profile, store: stray temp cleanup is unsafe across two Store instances; duplicate JSON configs collapse silently; one empty branch in Save; provider routing is returned as a shared pointer.
- gen: catch-all detection ignores `port: "0-65535"` and `network: "tcp"` forms; non-object provider routing is replaced instead of rejected; a provider inbound tagged `api`, `probe` or `devtools` matches our reserved inbound without a warning; presets emit no `dns` section (add in 1b if needed).
- geo/merge: duplicate load closures; no directory fsync after renames; only the first missing tag is reported; an `@attr`-only tag is skipped silently; no rename-failure test. The two-rename window can leave a mixed pair after a crash: 1b merges into a fresh directory and swaps it.
- validate: output buffer is unbounded before the 4 KB cap; the real `xray -test` is covered only under the `xrayint` tag (its error paths have a fake-binary unit test); protocol maps need a drift check on every Xray pin bump (add to the pin-bump checklist); scrubbing also masks non-secret routing values such as `geosite:cn`; secrets shorter than 6 characters (REALITY shortId) are not masked; a message for invalid JSON is not scrubbed.
- xrayrun: backoff steps 16 s and 30 s are unreachable; `TestBackoffWindowSlides` asserts nothing (the spawn-failure hang was fixed in 8528d14); duplicated test helpers; a Stop error is ignored in Latency; latency fails for all outbounds when one is invalid (1b falls back to one run per outbound).
- Follow-ups promised by the README: 1b creates new subscriptions with `SendDevice=true` (ADR 0008; the code zero value is false, `internal/profile/profile.go:61`); 1c adds the "send device info" switch in subscription settings; 1b adds the connectivity check through the tunnel.
- e2e: server rules are not strictly narrow (add a trailing block-all); one assertion depends on exact spacing; the log-ready check matches any " started"; port races; the access log is proof only on Windows and macOS.
- secretscan fix: a mixed-case allow entry; `enable-hooks.sh` does not use `--local`; `TestRunStagedExitCode` does not cover the allow path.

## Next: stage 1b and 1c

1b: new subscriptions get `SendDevice=true` and the connectivity check through the tunnel is added; zatvord on Windows (state and local API, single instance, journal and revert, system proxy, port picker, key providers, scheduled updates, geo bootstrap, dev-tool routing, supervisor, diagnostics, CLI). 1c: Tauri tray and panel, first run, MSI, stage 1 acceptance from Artur's home internet.

Stage 1 overall:

Windows VPN client: import all formats, geo, system proxy, routing, basic tray and panel, subscription
updates, crash recovery and rollback, diagnostics, CLI `status` and `check`.
Acceptance: Artur's test subscription imports; Hysteria2-DNS, XHTTP and Vision connect; Artur's
home-internet check.
Open for planning: Artur has no interactive Windows device, so stage 1 must say where Windows checks run
(CI runner, cloud Windows VM, or moved to the Mac). This affects the acceptance above, the tray placement
check (ADR 0005) and the Hyper-V check (ADR 0003).
