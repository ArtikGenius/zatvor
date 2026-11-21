# Stage 0c: Prototypes and Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the Zatvor repository foundation and run the stage 0c prototypes (Xray fixtures incl. legacy-noise migration, safe mode, Windows system-test CI job, TUN A vs B, UI framework, installer), ending with an ADR for every decision.

**Architecture:** One public Go monorepo. Pure, side-effect-free Go packages are built and tested in the cloud and in GitHub Actions (Linux/Windows/macOS). Everything that touches Windows state runs only on disposable GitHub Actions Windows runners (admin, discarded after each job) [DECIDED 2026-11-21: Artur's PC can't be rebooted now, so Windows Sandbox can't be enabled; Sandbox harness deferred until it can]. Nothing runs on Artur's PC in 0c except, with his OK, the UI prototype. Prototypes live under `proto/` and are throwaway; only `internal/`, `tools/`, `scripts/` and `docs/` carry into stage 1.

**Tech Stack:** Go (latest stable at Task 1, pinned via `toolchain` in `go.mod`), Xray-core v26.11.9 official binaries, `github.com/xtls/xray-core/common/geodata` protobuf types (MPL-2.0, dev/test use), sing-tun (prototype B), Tauri 2 and Wails v3 (UI prototypes), PowerShell 5.1 + Pester 5, Inno Setup 6 and WiX v5 (installer prototypes), GitHub Actions, gitleaks (CI).

**Spec:** `docs/superpowers/specs/2026-11-19-zatvor-design.md` (approved; mockups approved 2026-11-19 at https://claude.ai/artifact/WoaYqZzwiHj8adNRapwGuj). Requirements source: `brief/BRIEF.md` §9–§14 (not committed).

## Where each task runs

| Task | Where | Needs from Artur |
|---|---|---|
| 1–6 | cloud session + GitHub Actions | the empty public repo `zatvor` on GitHub |
| 7–8 | GitHub Actions `windows-latest` (job `systemtest-windows`) | — |
| 9 | CI builds and metrics on `windows-latest`; Artur tries the macOS build on his Mac from CI artifacts | Mac checklist; Windows tray on his host only if he agrees |
| 10 | CI (Pester) + install/uninstall on `windows-latest` | — |
| 11 | cloud | review of ADRs and the stage report |

Tools on Artur's PC are not needed in 0c. Windows Sandbox (one reboot) is enabled later when convenient; the Sandbox harness is then added as a follow-up.

## Global Constraints

- Artur's PC: never touch the other VPN; never change system proxy, routes, DNS, adapters, TUN on the host.
- Host user settings are off-limits: `~/.claude/settings.json`, shell/PowerShell profiles, `HKCU\Environment`, VS Code and Codex configs, autostart, URL schemes, shortcuts, Credential Manager.
- Never freeze or close a real process; never select processes by name on the host. Freezing code refuses any PID the test did not spawn (parent PID + creation time + per-test token in env).
- Any built Zatvor binary runs on the host only with safe mode on. Prototypes (TUN, UI, installer) and `systemtest` binaries run only on GitHub Actions Windows/macOS/Linux runners; the UI prototype may run on Artur's host only after he says so.
- Nothing from `/home/claude/refs/*` or `research/src/*` is executed or copied. Kordon code only from `e01c7c4`/`df69bfb` with `path@commit` in `NOTICE`; no proxy-tunnel code; no Kordon name, logo, texts or look.
- No telemetry, cloud, referral links.
- The dev subscription link is never committed, logged or written to repo files. It is not needed in 0c.
- `brief/` is in `.gitignore`. Secret scanning runs in pre-commit and CI.
- Xray pin: exactly `v26.11.9`; binaries verified against the release `.dgst` SHA-256.
- Licence GPL-3.0; every third-party component listed in `NOTICE`.
- Docs, code, comments, commits in English. README in Russian and English.
- `go test ./...` has no side effects: temp dirs, random free ports, no system changes. Real system changes only under `//go:build systemtest`, run only in Sandbox/VM/CI.
- Each commit ends with the attribution lines from the session reminder.

## Review Focus

1. **A fixture or doc carries a real secret** (a real subscription URL, a real VLESS UUID, a real server IP). Expected: pre-commit and CI fail with the file and line. Pinned by Task 1 `TestScanFlagsSubscriptionURL` and `TestScanFlagsUnknownUUID`.
2. **The Xray download is truncated, tampered or the `.dgst` is missing.** Expected: no binary is left in place, error names the asset. Pinned by Task 3 `TestFetchRejectsHashMismatch`, `TestFetchLeavesNoPartialFile`.
3. **A config the migration can't translate safely** (noise already in finalmask, chained dialer proxies, `applyTo`, redirect to :53, freedom with other settings). Expected: config returned unchanged with a refusal reason, never a half-migrated config. Pinned by Task 4 table test `TestMigrateRefuses`.
4. **A `systemtest` binary is started outside CI** (e.g. on Artur's host). Expected: it refuses to run unless `CI=true` and `ZATVOR_ALLOW_LIVE=1` are both set, exit code 2. Pinned by Task 7 `TestSystemtestRefusesOutsideCI`.
5. **PID reuse between "spawned by test" and "act on it".** Expected: the guard refuses when creation time differs. Pinned by Task 6 `TestGuardRefusesReusedPID`.

---

### Task 1: Repository foundation, secret scan, CI

**Files:**
- Create: `go.mod` (module `github.com/<owner>/zatvor`, owner taken from the repo Artur creates), `LICENSE` (GPL-3.0 text), `NOTICE`, `README.md` (Russian, "в разработке"), `README.en.md`, `.gitignore`, `.gitattributes` (`* text=auto eol=lf`, `*.ps1 eol=crlf`), `CHANGELOG.md`, `docs/STATUS.md`, `AI-SETUP.md` (stub: what exists today, honest "nothing to install yet")
- Move into repo: `docs/superpowers/specs/…`, `docs/superpowers/plans/…`, `docs/known-pitfalls.md`, `docs/research/**`
- Create: `tools/secretscan/main.go`, `tools/secretscan/scan.go`, `tools/secretscan/scan_test.go`, `tools/secretscan/allow.txt`
- Create: `.githooks/pre-commit` (sh), `scripts/dev/enable-hooks.ps1` (runs `git config core.hooksPath .githooks` — repo-local, not a user setting)
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Produces: `secretscan.Scan(path string, content []byte, allow AllowList) []Finding`; `type Finding struct{ Path string; Line int; Rule string }`; CLI `go run ./tools/secretscan [--staged] [paths...]`, exit 1 on findings.

- [ ] **Step 1: Write failing tests** in `tools/secretscan/scan_test.go`:
  - `TestScanFlagsSubscriptionURL`: `https://panel.real-host.ru:2096/sub/abc123` → 1 finding, rule `subscription-url`. `https://sub.example.net/sub/k2f9` → 0 (example domains `example.net|example.com|example.org` allowed).
  - `TestScanFlagsUnknownUUID`: `vless://3f1c…@1.2.3.4:443` with a UUID not in `allow.txt` → rule `uuid`; UUIDs listed in `allow.txt` → 0.
  - `TestScanFlagsPublicIPv4InFixtures`: a public IPv4 in `testdata/**` not in `allow.txt` → rule `ipv4`; RFC 5737 (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`), private and loopback → 0. Rule applies to `testdata/**` only.
  - `TestScanFlagsPrivateKeys`: `"privateKey": "<44 base64 chars>"` and `-----BEGIN … PRIVATE KEY-----` → findings.
- [ ] **Step 2:** `go test ./tools/secretscan/...` → FAIL (undefined).
- [ ] **Step 3: Implement** `Scan` with regex rules above; `AllowList` loaded from `allow.txt` (one literal per line, `#` comments).
- [ ] **Step 4:** `go test ./tools/secretscan/...` → PASS.
- [ ] **Step 5: CI** `ci.yml`: on push/PR; jobs `test` (matrix `ubuntu-latest`, `windows-latest`, `macos-latest`: `go vet ./...`, `go test ./...`), `secrets` (gitleaks action + `go run ./tools/secretscan .`). No other jobs yet.
- [ ] **Step 6: Repo files.** `.gitignore`: `brief/`, `/bin/`, `/dist/`, `*.exe` (outside `testdata`), `/third_party/xray/bin/`, `node_modules/`, `target/`. `NOTICE`: Xray-core (MPL-2.0, binary, unmodified), roscomvpn geoip/geosite (source URLs, no licence file, risk accepted), v2fly domain-list-community (MIT). `docs/STATUS.md`: stage 0c in progress, link to this plan.
- [ ] **Step 7: Verify** `go run ./tools/secretscan .` over the whole tree → exit 0 (docs/research must pass; anything flagged is fixed or added to `allow.txt` with a comment why it is fake).
- [ ] **Step 8: Commit** `chore: repository foundation, secret scan and CI`; push to `main` once Artur's repo is attached (the only direct push to `main`); CI green on all three OSes. Tasks 2–11 go on branch `stage-0c`.

### Task 2: Test fixtures and synthetic geo databases

**Files:**
- Create: `testdata/fixtures/{xray-hy2-dns-legacy.json,xray-hy2-dns-finalmask.json,happ-routing.json,happ-routing-link.txt,amneziawg.conf}` (copied from `brief/fixtures/`, after Task 1 scan passes on them)
- Create: `internal/geo/tags.go`, `internal/geo/tags_test.go`, `internal/geo/synth/synth.go`, `internal/geo/synth/synth_test.go`

**Interfaces:**
- Produces: `geo.NeededTags(cfg []byte) (site []string, ip []string, err error)` — sorted, deduplicated tag names (without `geosite:`/`geoip:` prefix) referenced from `routing.rules[].domain|ip` and `dns.servers[].domains|expectIPs`; `ext:` references returned as error `ErrExtRef` (not supported in fixtures).
- Produces: `synth.Write(dir string, site, ip []string) (sitePath, ipPath string, err error)` — writes `geosite.dat`/`geoip.dat` (`geodata.GeoSiteList`/`GeoIPList`), each tag with one synthetic entry: site tag `t` → domain `t.zatvor-test.invalid`; ip tag `t` → a /32 from `198.18.0.0/15` derived from tag index.

- [ ] **Step 1: Failing tests:**
  - `TestNeededTagsLegacyFixture`: legacy fixture → site includes `category-ru`, `whitelist`, `win-spy`, `torrent`, `twitch-ads`, `private`; ip includes `direct`, `private` (assert exact lists after first run is inspected and copied into the test as the golden list).
  - `TestNeededTagsFinalmaskFixture`: ip includes `ru`.
  - `TestSynthRoundTrip`: `Write` then unmarshal both files → every requested tag present exactly once, uppercase tag codes as Xray stores them.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement** (JSON walk with `encoding/json` into `any`; protobuf via `google.golang.org/protobuf/proto`).
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: Commit** `test: fixtures and synthetic geo databases`.

### Task 3: Pinned Xray fetch and verification

**Files:**
- Create: `third_party/xray/pin.json`, `tools/xrayfetch/main.go`, `tools/xrayfetch/fetch.go`, `tools/xrayfetch/fetch_test.go`

**Interfaces:**
- Produces: `pin.json` = `{"version":"v26.11.9","assets":{"windows-amd64":{"zip":"Xray-windows-64.zip","sha256":"…"},"windows-arm64":…,"linux-amd64":…,"linux-arm64":…,"darwin-amd64":…,"darwin-arm64":…}}` with SHA-256 values taken from the release `.dgst` files during this task.
- Produces: `xrayfetch.Fetch(ctx, client *http.Client, baseURL, platform, destDir string) (xrayPath string, err error)`; CLI `go run ./tools/xrayfetch -platform <os-arch> -dest third_party/xray/bin/<os-arch>`. Default `baseURL` `https://github.com/XTLS/Xray-core/releases/download/<version>/`.

- [ ] **Step 1: Failing tests** (all against `httptest.Server`):
  - `TestFetchVerifiesPinnedHash`: zip served whose SHA-256 matches pin and its `.dgst` → `xray`/`xray.exe` extracted, executable bit on Unix.
  - `TestFetchRejectsHashMismatch`: one byte changed → error contains asset name; `destDir` has no `xray*`.
  - `TestFetchRejectsDgstDisagreement`: zip matches pin but `.dgst` SHA-256 differs → error (both must agree).
  - `TestFetchLeavesNoPartialFile`: server closes connection mid-body → error, no file in `destDir`, temp file removed.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement**: download to `destDir/.tmp-*`, hash while streaming, compare with pin and `.dgst` line `SHA2-256= <hex>`, extract only `xray[.exe]`, `geoip.dat`, `geosite.dat` ignored (we use our own), atomic rename.
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: Fill `pin.json`** from the real `v26.11.9` `.dgst` files (download in the cloud, not on Artur's PC); run `go run ./tools/xrayfetch -platform linux-amd64 …` once and `xray version` → prints `Xray 26.11.9`.
- [ ] **Step 6: Commit** `build: pin Xray v26.11.9 with verified fetch`.

### Task 4: Legacy noise → finalmask migration

**Files:**
- Create: `internal/gen/noisemig/migrate.go`, `internal/gen/noisemig/migrate_test.go`, `testdata/golden/noisemig/legacy-to-finalmask.json` (from `docs/research/translated/legacy-to-finalmask.json`)

**Interfaces:**
- Produces: `noisemig.Migrate(cfg []byte) (out []byte, rep Report, err error)`; `type Report struct{ Migrated []string /*outbound tags*/; Refused []Refusal }`; `type Refusal struct{ Tag, Reason string }`. Reasons are constants: `ReasonNoiseInFinalmask`, `ReasonChainedDialer`, `ReasonApplyTo`, `ReasonRedirect53`, `ReasonFreedomHasOtherSettings`.
- Rules (spec §6.4): for each outbound with `streamSettings.sockopt.dialerProxy` → freedom outbound with non-empty `settings.noises`: append `{"type":"noise","settings":{"noise":[…translated items…]}}` as the **last** item of `streamSettings.finalmask.udp`; remove `dialerProxy`; remove the freedom outbound if no other reference; drop empty `noises` arrays elsewhere. Item translation copied from `docs/research/xray-core.md` §3 (hex packet unchanged, `delay` kept). Output JSON key order: input order preserved (use an ordered JSON representation, not `map`).

- [ ] **Step 1: Failing tests:**
  - `TestMigrateLegacyFixtureMatchesGolden`: semantic JSON equality with the golden file; `rep.Migrated == ["proxy"]` (actual tag from fixture).
  - `TestMigrateKeepsPacketBytes`: the `a3c1…5db8d70e` hex packet appears unchanged in output.
  - `TestMigrateFinalmaskFixtureIsNoop`: finalmask fixture → output byte-identical, empty report.
  - `TestMigrateRefuses`: table with one case per reason → output byte-identical to input, one `Refusal` with that reason.
  - `TestMigrateIdempotent`: `Migrate(Migrate(x))` = `Migrate(x)`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: Commit** `feat(gen): legacy noise to finalmask migration (off by default)`.

### Task 5: Xray runs both fixtures (integration)

**Files:**
- Create: `internal/xrayint/xrayint_test.go` (`//go:build xrayint`), `internal/xrayint/udpsink.go`
- Modify: `.github/workflows/ci.yml` (job `xray-integration` on `ubuntu-latest` and `windows-latest`: `xrayfetch` then `go test -tags xrayint ./internal/xrayint/...`)

**Interfaces:**
- Consumes: `xrayfetch.Fetch`, `geo.NeededTags`, `synth.Write`, `noisemig.Migrate`.
- Produces: test helper `rewriteServer(cfg []byte, tag, addr string) []byte` (test-only, rewrites the hysteria2 outbound's server address to the local sink).

- [ ] **Step 1: Tests:**
  - `TestXrayAcceptsLegacy`, `TestXrayAcceptsFinalmask`, `TestXrayAcceptsMigrated`: synth geo files for the config's tags in a temp dir, `XRAY_LOCATION_ASSET=<dir>`, config on stdin, `xray run -test -format json` → exit 0. Legacy: stderr contains the deprecation warning; migrated and finalmask: no `deprecated`.
  - `TestMigratedWireEquivalent`: start a UDP sink on `127.0.0.1:0`, point the hysteria2 outbound there, run Xray (legacy config) with the SOCKS inbound on a free port, send one UDP-associate request through it; capture the first datagram. Repeat with migrated config. Assert: first datagram in both = the fixture's hex packet; the second datagram in both is not the noise packet (start of QUIC/salamander). Timeout 10 s.
- [ ] **Step 2:** run locally in the cloud with `-tags xrayint` → PASS for all four (tests are written against existing code, so a failure here is a real finding: stop and report).
- [ ] **Step 3: Commit** `test: Xray v26.11.9 runs legacy, finalmask and migrated fixtures`; CI job green on Linux and Windows.

### Task 6: Safe mode, system-change recorder and test process guard

**Files:**
- Create: `internal/safemode/safemode.go`, `internal/safemode/safemode_test.go`, `internal/safemode/default_dev.go` (`//go:build !release`), `internal/safemode/default_release.go` (`//go:build release`)
- Create: `internal/sysapi/sysapi.go` (interfaces), `internal/sysapi/recorder.go`, `internal/sysapi/recorder_test.go`
- Create: `internal/proc/testguard/guard.go`, `guard_test.go`, `guard_windows.go`, `guard_unix.go`
- Create: `cmd/zatvord/main.go` (only `--version`, `--safe`, `--print-mode`; nothing else in 0c)

**Interfaces:**
- Produces: `safemode.Resolve(args []string, env func(string) string) (Mode, Source)`; `Mode` is `Safe|Live`; sources: flag `--safe`/`--live`, env `ZATVOR_SAFE=1|0`, build default. Dev builds (`!release`) default to `Safe` and `--live` is refused unless env `ZATVOR_ALLOW_LIVE=1` is also set (only the CI systemtest job sets it).
- Produces: `sysapi.SystemProxy{ Get() (ProxyState, error); Set(ProxyState) error }`, `sysapi.ProcessControl{ Suspend(ProcID) error; Resume(ProcID) error; Terminate(ProcID) error }`, `type ProcID struct{ PID int; Created time.Time }`. `sysapi.NewRecorder(w io.Writer) *Recorder` implements both: every call appends one JSON line `{"ts","op","args"}` and changes nothing; `Get` returns the last recorded state or zero.
- Produces: `testguard.Spawn(t *testing.T, name string, args ...string) ProcID` (sets `ZATVOR_TEST_TOKEN=<random>` in the child env) and `testguard.Check(id ProcID) error` → nil only if the process exists, its creation time equals `id.Created`, its parent is the test process, and its env token matches. Windows reads the token via the process's own handshake file `%TEMP%/zatvor-guard-<pid>` written by the child helper `testguard/child` (reading another process's env is not used).

- [ ] **Step 1: Failing tests:**
  - `TestResolveDevDefaultsSafe`, `TestResolveLiveRefusedWithoutAllow`, `TestResolveEnvOverridesDefault`, `TestResolveFlagWinsOverEnv`.
  - `TestRecorderWritesAndChangesNothing`: `Set` then `Get` returns recorded state; log has 2 lines with ops `proxy.set`, `proxy.get`.
  - `TestGuardAcceptsOwnChild`; `TestGuardRefusesForeignPID` (PID of `os.Getppid()`); `TestGuardRefusesReusedPID` (correct PID, `Created` shifted by 1 s); `TestGuardRefusesExited`.
  - `TestZatvordPrintsSafeMode`: `go run ./cmd/zatvord --print-mode` → `safe (build default)`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3: Implement.** Creation time: Windows `GetProcessTimes`, Linux `/proc/<pid>/stat` field 22, macOS `sysctl kern.proc.pid`.
- [ ] **Step 4:** run on all three OSes in CI → PASS.
- [ ] **Step 5: ADR** `docs/decisions/0004-safe-mode-and-test-guard.md`.
- [ ] **Step 6: Commit** `feat: safe mode, recorder and test process guard`.

### Task 7: Windows system-test job in CI and first systemtest

**Files:**
- Create: `internal/systest/gate.go` (`//go:build systemtest`), `internal/systest/gate_test.go`
- Create: `internal/proc/freeze/freeze_windows.go`, `internal/proc/freeze/freeze_systemtest_test.go` (`//go:build systemtest && windows`)
- Modify: `.github/workflows/ci.yml` (job `systemtest-windows` on `windows-latest`: `go test -tags systemtest ./...` with `ZATVOR_ALLOW_LIVE=1`; uploads `results/` as artifact)

**Interfaces:**
- Produces: `systest.Require(t *testing.T)` — called first in every systemtest; fails the test (and `TestMain` exits 2) unless env `CI=true` and `ZATVOR_ALLOW_LIVE=1`; logs the runner name. Every later systemtest package uses it.
- Produces: `freeze.Suspend(id sysapi.ProcID) error` / `Resume` via `NtSuspendProcess`/`NtResumeProcess` on a handle opened with `PROCESS_SUSPEND_RESUME|PROCESS_QUERY_LIMITED_INFORMATION`, after `testguard.Check` in tests.

- [ ] **Step 1: Failing tests:** `TestSystemtestRefusesOutsideCI` (re-exec the test binary with `CI` unset → exit 2, message "systemtest runs only on CI runners"); `TestSuspendResumeOwnChild` (spawn `testguard/child` printing a counter every 50 ms to a file; `Suspend`; counter stops for 1 s; `Resume`; counter advances); `TestSuspendRefusesForeign` (`ProcID` failing `testguard.Check` → error, target keeps running).
- [ ] **Step 2:** run → FAIL; implement; push; `systemtest-windows` job → PASS.
- [ ] **Step 3: Commit** `test: Windows systemtest job and first freeze test`.
- [ ] **Follow-up (not in 0c):** Windows Sandbox harness (`.wsb` + `Invoke-SandboxTest.ps1`) once Artur can reboot to enable Sandbox.

### Task 8: TUN front prototype A vs B (CI Windows runner)

**Files:**
- Create: `proto/tun/a/config.go` (writes an Xray config with `tun` inbound: `autoSystemRoutingTable`, `autoOutboundsInterface`, `autoSystemWfpBlockLeak` on Windows, outbound socks → upstream), `proto/tun/b/main.go` (sing-tun stack, strict route, forwarding to Xray socks inbound), `proto/tun/upstream/config.json` (Xray socks server + freedom, runs on the same runner as the "remote"), `proto/tun/check/Check.ps1`, `proto/tun/RESULTS.md`
- Create: `docs/decisions/0003-tun-front.md`

**Interfaces:**
- Produces: `Check.ps1 -Variant A|B` → `results\tun-<variant>.json` with fields: `dnsLeakV4`, `dnsLeakV6`, `bypassPackets` (packets on the physical adapter not to the upstream, from `pktmon` capture during a scripted curl/nslookup run), `killRecovery` (after `Stop-Process -Force` on the TUN owner: internet back within 5 s, adapter removed, routes restored), `adapterLeftovers`, `throughputMbps` (local iperf-style transfer through the tunnel, 30 s), `cpuIdlePct` (60 s), `perAppRule` (rule by process path for `curl.exe` sends it direct while `nslookup` goes via tunnel).

- [ ] **Step 1:** implement A and B configs/programs; build in CI for `windows-amd64` (B depends on sing-tun; record its licence in `NOTICE`).
- [ ] **Step 2:** run `Check.ps1 -Variant A` and `-Variant B` in the `systemtest-windows` CI job (manual trigger, results uploaded as artifact); results JSON copied into `RESULTS.md` tables.
- [ ] **Step 3:** sleep/wake cannot be tested on a CI runner: recorded as "not measured in 0c, moved to stage 3 Hyper-V VM check".
- [ ] **Step 4: ADR 0003**: decision by the spec §4.3 rule: A only if it passes every measured row; otherwise B. Include the raw numbers.
- [ ] **Step 5: Commit** `proto: TUN front A vs B with results`.

### Task 9: Tray + popup prototype, Tauri 2 vs Wails v3

**Files:**
- Create: `proto/ui/shared/popup.html` (static copy of the approved "Main" mockup: iris, status, three app rows; Onest bundled locally, no Google Fonts call), `proto/ui/tauri/**`, `proto/ui/wails/**`, `proto/ui/RESULTS.md`, `.github/workflows/proto-ui.yml` (manual trigger; builds both for `windows-amd64`, `darwin-universal`; uploads artifacts)
- Create: `docs/decisions/0005-ui-framework.md`

**Interfaces:**
- Both prototypes: tray icon (small iris), left click toggles a 360×560 borderless popup anchored to the tray icon, pre-created hidden at start; closes on focus loss; system dark/light; no console window; writes `metrics.json` next to the exe: `openMs` (click → first paint, median of 20), `hiddenRssMB` after 60 s, `binaryMB`, `childProcesses` (names + PIDs, e.g. WebView2).
- Wails v3: first check the current release status; if it is still alpha, it stays in the comparison but cannot win unless Tauri fails a hard criterion.

- [ ] **Step 1:** implement both; CI builds green.
- [ ] **Step 2:** run both on `windows-latest` (the runner has an interactive desktop session; if tray clicks can't be scripted there, measure `openMs` via the prototype's own `--self-test` that toggles the popup from code) and collect `metrics.json`. Ask Artur whether to also run on his host taskbar (no system changes, but it is a window from a build).
- [ ] **Step 3:** Mac checklist for Artur (5 lines: open from menu bar, placement under notch, dark/light switch, Gatekeeper prompt text, `metrics.json` path).
- [ ] **Step 4: ADR 0005** using spec §4.4 criteria and §12 budgets (popup ≤ 150 ms, hidden UI target ≤ 120 MB).
- [ ] **Step 5: Commit** `proto: tray popup in Tauri 2 and Wails v3 with metrics`.

### Task 10: Installer format and one-line install script

**Files:**
- Create: `scripts/install/install.ps1`, `scripts/install/Ed25519.ps1`, `scripts/install/tests/Ed25519.Tests.ps1`, `scripts/install/install.sh`, `proto/installer/inno/zatvor.iss`, `proto/installer/wix/Package.wxs`, `proto/installer/RESULTS.md`
- Create: `docs/decisions/0006-installer-and-install-script.md`

**Interfaces:**
- Produces: `Test-Ed25519Signature -PublicKey <byte[32]> -Message <byte[]> -Signature <byte[64]> → [bool]` in pure PowerShell 5.1 on `System.Numerics.BigInteger` (RFC 8032 verify; SHA-512 via `System.Security.Cryptography.SHA512`).
- `install.ps1`: downloads `SHA256SUMS` + `SHA256SUMS.sig` + the installer, verifies signature with the embedded public key, then hash, then runs the installer elevated. In 0c it points at a local test directory (`-Source <path>`), no network release exists.

- [ ] **Step 1: Failing Pester tests:** RFC 8032 §7.1 test vectors 1, 2, 3 → `$true`; vector 2 with one message bit flipped → `$false`; signature with `S ≥ L` → `$false`; runs on PowerShell 5.1 (`windows-latest` has it) and pwsh 7.
- [ ] **Step 2:** run → FAIL; implement; → PASS. Measure verify time on 5.1 (target < 2 s).
- [ ] **Step 3:** `install.sh` uses `openssl pkeyutl -verify -rawin`; refuses with instructions when `openssl` lacks Ed25519 (test in CI on `macos-latest` and `ubuntu-latest`).
- [ ] **Step 4:** Inno and WiX prototypes install a dummy `zatvor.exe` machine-wide into Program Files with an uninstall entry and a placeholder for the stage 3 service; build both in CI; install + uninstall each on `windows-latest`; record: size, admin prompt count, silent install flags, upgrade-in-place behaviour, leftovers after uninstall.
- [ ] **Step 5: ADR 0006.**
- [ ] **Step 6: Commit** `proto: installer formats and signed install script`.

### Task 11: Remaining ADRs, stage report, review

**Files:**
- Create: `docs/decisions/0001-go-daemon-and-components.md`, `0002-xray-separate-process.md`, `0007-xray-pin-and-geo-merge.md`, `0008-hwid-algorithm.md` (frozen forever, R3.12), `0009-local-api.md`, `docs/decisions/README.md` (index)
- Modify: `docs/STATUS.md`, `CHANGELOG.md`, spec status line
- Create: `docs/checklists/stage-0c.md` (what Artur checks: CI links, Windows runner results, Mac tray checklist)

- [ ] **Step 1:** write ADRs from spec §4.1, §4.2, §5.5, §6.5, §4.5 (context, decision, consequences, links to research).
- [ ] **Step 2:** `superpowers:requesting-code-review` over the whole stage branch; fix findings.
- [ ] **Step 3: Verify** CI green on all jobs; `go run ./tools/secretscan .` exit 0; every ADR listed in the index.
- [ ] **Step 4: Commit** `docs: stage 0c ADRs and status`; open PR `stage-0c` → `main` for Artur.
