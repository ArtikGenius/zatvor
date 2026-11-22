# Status

Stage 1b (zatvord on Windows) is complete as of 2026-11-22 on branch `stage-1b`, pending the 1b final fix wave and the merge to main. Next: stage 1c (UI and delivery). Stage 1a is complete and merged. Stage 0c is complete, pending the macOS UI check.
Plan of 1b: [docs/superpowers/plans/2026-11-22-stage-1b-zatvord-windows.md](superpowers/plans/2026-11-22-stage-1b-zatvord-windows.md).
What to check: [docs/checklists/stage-1b.md](checklists/stage-1b.md) (nothing to run by hand).
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

## Done in 1b

- `appdir`, `settings`: per-user data directories with one full-control ACE, versioned settings.
- `ports`: persisted loopback ports, a busy one re-picked.
- `journal`, `sysproxy`: fsynced change journal with safe revert; journaled Windows system proxy with conflict detection ([ADR 0012](decisions/0012-journal-and-revert.md)).
- `store` keys, `geo/assets`, `tools/geofetch`: DPAPI and Credential Manager key providers; pinned bundled geo and a merged-set cache.
- `engine`, `xrayrun`: connect through the system proxy with the tunnel check and fail policy; job object kill-on-close.
- `subsvc`, `devtools`: subscription service with tunnel-only scheduled updates; journaled dev-tool routing ([ADR 0013](decisions/0013-devtool-routing-targets.md)).
- `api`, `instance`, `daemon`, `supervisor`: local API v1, single instance, `zatvord`, crash supervisor.
- `cmd/zatvord`, `cmd/zatvor`, `diag`: the daemon, the CLI (`status`, `check`, `diag`) and the redacted diagnostics bundle.
- Live acceptance on `windows-latest`: [45048050788](https://github.com/ArtikGenius/zatvor/actions/runs/45048050788) (9d97c48) and [45049736536](https://github.com/ArtikGenius/zatvor/actions/runs/45049736536) (6c0c2c9), attempts 1 and 2 each, all green.
- Ledger with rulings R1-R55 and per-task reports: `.superpowers/sdd/2026-11-22-stage-1b-zatvord-windows/`.

## Known limitations in 1b

- Without autostart, a killed process tree or a power loss leaves the system proxy on a dead port until the next launch (spec §8). Revisit with autostart in 1c.
- A dev-tools toggle while connected applies at the next connect.
- Xray and the bundled geo files are expected next to the executable (`XrayPath`, `BundledGeo`). This matters for the installer layout in 1c.
- Diagnostics do not redact standalone secrets shorter than 6 bytes, such as a very short Reality shortId shown alone (R52).
- The local API has no `GET /v1/settings` yet (1c).
- The `Local\` single-instance mutex is per session: RDP plus a console session can run two daemons.
- Safe and live modes keep separate stores and journals (R43, R16): switching modes shows a different, empty subscription list.

## Scheduled for the 1b final fix wave

These three are fixed before the PR merges:

- `subsvc`: a failed `Start` refresh blocks scheduled updates for a full interval (seed the backoff instead).
- `xrayrun` latency test: robust against a parallel package grabbing the port (R47 fixed the runner; the test's own helper is the open part).
- `engine` ready checks (`engine.go:136`) use a plain `DialReady` on bind-then-close ports; use the protocol-aware check (mixed = SOCKS/HTTP, probe = SOCKS).

The final reviewer also checks that `profileGeo` source selection in `engine` agrees with the `gen` routing source.

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
- [0012](decisions/0012-journal-and-revert.md) Journal and revert semantics
- [0013](decisions/0013-devtool-routing-targets.md) Dev-tool routing targets

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

Deferred minor review findings from 1b:
- appdir, settings: `Resolve` tightens permissions on any `--data-dir` (daemon validates it); the every-field guard skips struct subfields; a dir fsync error is ignored on Windows.
- ports: `TestExhausted` should assert calls > 0; `TestLoopbackProber` should hold a real listener (TCP-only and UDP-only cases) and not skip on error; nil `rnd` doc note.
- journal: `done()` re-reads the whole file (O(n^2)); no test for `RevertAll` when `pending()` fails; the Reverter doc should say Current must encode like Applied (literal compare with UseNumber); `pending()` skips unparsable mid-file lines silently.
- sysproxy: the named RAS connection path is never exercised on the runner; a possible auto-detect flake in the foreign-PAC systemtest; `setConn` retry discards the first error and can leave a half state on double failure; `getConn` falls back to FLAGS on any error.
- store keys: in-process CredMan first-write race (needs a mutex); the `CredRead` blob is not zeroed before `CredFree`; the `CredFree` result is discarded; the DACL test should assert the mask.
- geo/assets: mutex held across a download; `url+".sha256"` is wrong for URLs with query strings; `Prepare` re-hashes inputs on reuse; a cached profile file replaces the bundled roscomvpn one (a tag only in the bundle then goes missing); the `Fetch` swap loop is non-transactional across the two files (safe through `cachedPath`); no test for "already cached, `NeedsBootstrap` false".
- engine: a geo switch cannot be interrupted after the old runner stopped (latency only).
- subsvc: `Start` joining a User flight does not record the attempt; a `Run` cancel mid-flight logs a false failure and a detached flight may save during shutdown; `TestButtonNeverJoinsScheduledFlight` needs a deadline; the per-user settings snapshot of a profile could be lost by future direct store edits.
- devtools: an empty object with a comment is cosmetic; a new-file create could use a no-replace rename; the `.ps1` wrapper does not run under a Restricted policy (UI note in 1c).
- api: test globals are restored before `srv.Stop` (narrow race window); the local API has no `GET /v1/settings` (1c); the `Local\` mutex is per session.
- xrayrun: the `TestSocksReady` late-listener subtest rebinds a freed port.
- daemon, e2e: `saveRunner` and the gate are duplicated across the live-test packages.

## Next: stage 1c

1c, UI and delivery:
- The Tauri tray and popup on the local API: styles as classes, the origin added to the API allow-list, and a named pipe reconsidered (ADR 0009).
- Panel basics: overview, profiles and subscriptions, settings, about.
- First run: paste the link, then a warning that the first request registers a device, then connect.
- The "send device info" switch, the Happ hint card (§5.3) and the update diff.
- ru/en and light/dark.
- MSI (WiX v5), machine-wide, with autostart and an installer layout that puts Xray and geo next to `zatvord.exe`.
- Stage 1 acceptance with Artur: his test subscription imports, and Hysteria2-DNS, XHTTP and Vision connect from his home internet.

Stage 1 overall:

Windows VPN client: import all formats, geo, system proxy, routing, basic tray and panel, subscription
updates, crash recovery and rollback, diagnostics, CLI `status` and `check`.
Acceptance: Artur's test subscription imports; Hysteria2-DNS, XHTTP and Vision connect; Artur's
home-internet check.
Open for planning: Artur has no interactive Windows device, so stage 1 must say where Windows checks run
(CI runner, cloud Windows VM, or moved to the Mac). This affects the acceptance above, the tray placement
check (ADR 0005) and the Hyper-V check (ADR 0003).
