# Stage 1b: zatvord on Windows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A headless `zatvord` that imports and updates subscriptions, connects through the Windows system proxy, routes developer tools through the tunnel, reverts everything it changed after exit or crash, and answers a local API and the `zatvor status`/`check` CLI. It is proven live on GitHub's Windows runners.

**Architecture:** 1b wires the 1a libraries into a daemon. Every system change goes through a `sysapi` interface. Each interface has a live Windows implementation, used only under the `systemtest` tag on CI, and the `Recorder`, which safe mode uses. Every change is journaled and fsynced before it is applied. The pure logic (journal, scheduler, engine state, dev-tool file edits) takes its clock, filesystem roots and dialers as parameters. 1c adds the Tauri UI on top of the local API.

**Tech Stack:** Go 1.27.4; `golang.org/x/sys/windows` (registry, DPAPI, Credential Manager, WinINet, job objects, mutex); `github.com/tailscale/hujson` (BSD-3-Clause) for JSONC with comments kept; the pinned Xray v26.11.9 binary.

**Spec:** `docs/superpowers/specs/2026-11-19-zatvor-design.md` (§4.1, §4.5–4.6, §5.6, §6.5, §7.7, §8, §9, §10, §16 Q2.10, R3.3, R3.5, R3.6, B3, B8). ADRs 0004, 0007, 0008, 0009, 0010, 0011. Pitfalls: `docs/known-pitfalls.md` K30, K31, K43, K50, 47, 53, 56, 57, 58. Deferred items: `docs/STATUS.md` "Follow-ups promised by the README" and "1b" notes.

## Global Constraints

- Go `go 1.27` + `toolchain go1.27.4`; run with `GOTOOLCHAIN=go1.27.4`. Module `github.com/ArtikGenius/zatvor`. Xray pinned to v26.11.9.
- `go test ./...` has no side effects and no network: temp dirs, random free loopback ports, fake clocks, fake registry/filesystem roots. A test that really changes the host carries the `systemtest` tag, calls `systest.Require(t)`, and has `TestMain` → `systest.Main(m)` (ADR 0004). It runs only in the `systemtest-windows` CI job.
- Safe mode is the default for every build (`safemode.Resolve`). In safe mode, every system change (system proxy, shell profiles, app configs, Credential Manager) goes to `sysapi.Recorder` or a recorder of the same shape. The daemon's own data directory is the only place it writes.
- Artur's PC: nothing in this stage runs live there. Windows checks run on GitHub `windows-latest` runners (Artur's decision).
- Before a system change, append a journal entry and fsync it: what, the previous value, how to revert. Revert happens on normal exit, on the next start after a crash, and from the supervisor (spec §8). Never revert a value that someone else changed after us. Keep theirs and warn instead.
- Foreign system proxy or PAC is never overwritten silently. Return a typed conflict, and the user (API) chooses (spec §8).
- Processes are only handled through the handle or PID we started. Nothing is selected or killed by name.
- Listeners: loopback only. The local API checks the token and `Host`/`Origin` (ADR 0009).
- Secrets (subscription URLs, keys, passwords, tokens) never reach logs, errors, API error bodies, diagnostics or test names. The profile store stays encrypted (R3.5). The Xray config goes over stdin only.
- Subscription requests: one user action = one request. When connected, requests go through the tunnel (the probe SOCKS dialer, R3.6). Without the tunnel, only the user's button triggers a request, with no automatic retries. Scheduled retries back off 1→2→4… min, capped at the update interval (spec §5.6).
- New subscriptions default to `SendDevice=true` (ADR 0008). HWID and device headers are never sent over plain `http://`; UA still is.
- No telemetry. Outbound hosts: the subscription URLs the user gave, geo URLs a profile names, the pinned geo/Xray release URLs (build tools only), and the IP-check services for the tunnel check (below).
- Files we edit for the user (shell profiles, `~/.claude/settings.json`, VS Code settings): write through symlinks, never replace the file. Use marked blocks or a single key, an atomic write with a backup, and an mtime+hash check with retry. Never write if the file changed between read and rename (spec §9).
- Errors carry typed codes. User strings (ru/en) are 1c. The API returns `{"code","message"}` with English developer text.

## Review Focus

1. The daemon is killed (`taskkill /F`, power loss) while the system proxy is ours. The next start, or the supervisor if the daemon cannot start, restores the user's previous proxy exactly, including a foreign PAC URL and the bypass list. Owners: Tasks 3, 4, 11, 13.
2. Another program changes the system proxy while we are connected. We detect it, warn, and on disconnect leave their value alone. We do not "restore" our stale previous value over theirs. Owners: Tasks 3, 4.
3. A file we edit for dev tools is a symlink, has CRLF/BOM/comments, or is changed by its owning app between our read and our rename. The link stays a link, the comments and other keys survive byte for byte, and a racing write is never lost. Owner: Task 9.
4. A scheduled update runs while the tunnel is down, offline, or the provider answers "device limit". It sends no request (or exactly one, for the button), does no retry burst and keeps the working profiles. Owner: Task 8.
5. A persisted inbound port is now taken by another app, or inside a Hyper-V/WSL excluded range. A new port is picked and the system proxy points at it. It is not a silent "connected" with a dead proxy. Owners: Tasks 2, 7.

---

## File Structure

```
internal/appdir/        data/cache/log dirs per user (%LOCALAPPDATA%\Zatvor), safe-mode sandbox dir
internal/settings/      versioned settings file (ports, active profile, dev tools on/off, update policy)
internal/ports/         loopback port picker with exclusion handling (pitfalls 57, 58, K50)
internal/journal/       fsynced change journal and revert registry
internal/sysproxy/      Windows system proxy (HKCU + WinINet refresh, RAS entries); conflict detection
internal/store/         + keys_windows.go: DPAPI file key, Credential Manager key
internal/geo/assets/    bundled geo pin, cache, per-tag-set merged dirs, profile geo URLs, bootstrap
tools/geofetch/         build-time fetch of pinned roscomvpn + v2fly dat files with SHA-256 check
internal/engine/        connect / disconnect / health; tunnel check; fail policy (spec §7.7)
internal/subsvc/        subscriptions: add, update, remove, scheduler, through-tunnel fetch
internal/devtools/      PowerShell / Git Bash blocks, Claude Code, VS Code, codex/gemini wrappers
internal/api/           local HTTP API v1 + discovery/token files + SSE
internal/instance/      single instance (named mutex) and "show" forwarding
internal/daemon/        assembly of all services; lifecycle; revert on start/exit
internal/supervisor/    supervisor mode: restart with backoff, revert when giving up
internal/diag/          diagnostics bundle with redaction
cmd/zatvord/            daemon + supervisor entry point
cmd/zatvor/             CLI: status, check, diag
```

---

### Task 1: App directories and settings

**Files:**
- Create: `internal/appdir/appdir.go`, `internal/appdir/appdir_test.go`, `internal/settings/settings.go`, `internal/settings/settings_test.go`

**Interfaces:**
- Produces:
  - `type appdir.Dirs struct { Data, Cache, Logs, Journal, Geo, Run string }`. `appdir.Resolve(base string) (Dirs, error)` creates them with 0700 or a user-only ACL. `appdir.Default(env func(string) string) (string, error)` returns `%LOCALAPPDATA%\Zatvor` on Windows, `~/Library/Application Support/Zatvor` on macOS and `$XDG_DATA_HOME/zatvor` on Linux. The `--data-dir` flag (Task 11) overrides it.
  - `type settings.Settings struct { Version int; Ports gen.Ports; ActiveProfile string; DevTools bool; LogLevel string; UpdateOnStart bool }`.
  - `settings.Load(path) (Settings, error)` returns defaults when the file is absent. `settings.Save(path, Settings) error` writes temp, fsyncs, renames, and keeps `.bak`.
  - `settings.ErrCorrupt` and `settings.ErrNewerVersion`.

- [ ] **Step 1: Failing tests.**
  - `TestRoundTripEveryField`: every field is set to a non-zero value, survives Save→Load, and is compared with reflect.DeepEqual (K31).
  - `TestNeverEmptyOverNonEmpty`: Save of the zero Settings over an existing non-empty file returns `settings.ErrRefuseEmpty` and the file is unchanged (K30).
  - `TestCrashBetweenSteps`: an unexported hook fails after the temp write, and the old file still loads.
  - `TestNewerVersionRefused`: a file with `Version: 999` returns `ErrNewerVersion`.
  - `TestDirsUserOnly` (non-Windows: mode 0700; Windows: owner-only DACL via `windows.GetNamedSecurityInfo`).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** (JSON with `"version":1`). **Step 4: Run** → PASS. **Commit** `feat(appdir,settings): per-user dirs and versioned settings`.

### Task 2: Port picker

**Files:**
- Create: `internal/ports/ports.go`, `internal/ports/ports_test.go`

**Interfaces:**
- Consumes: `gen.Ports`.
- Produces: `ports.Pick(prev gen.Ports, probe Prober, rnd *rand.Rand) (gen.Ports, bool, error)`. The bool is true when any port changed. `type Prober func(port int) error` binds `127.0.0.1:port` TCP (and UDP for Mixed). `ports.LoopbackProber` is the real one. `ports.ErrExhausted`.

- [ ] **Step 1: Failing tests** (fake Prober):
  - `TestKeepsWorkingPorts`: every previous port binds, so the result is unchanged and the bool false (R3.3).
  - `TestRepicksOnlyTheBusyOne`: one port fails with `EADDRINUSE` or `WSAEACCES` (the Hyper-V excluded range, pitfall 57). Only that port changes.
  - `TestAvoidsWellKnown`: a pick never returns 10808, 10809, 7890, 9090, 9097, 19090 or a port below 20000 (pitfalls 57, 58).
  - `TestSpread`: four fresh picks are at least 1024 apart from each other (K50, so one reserved block cannot take two).
  - `TestExhausted`: a Prober that always fails gives `ErrExhausted` after at most 64 tries.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** (random in [20000, 60999]). **Step 4: Run** → PASS. **Commit** `feat(ports): persisted loopback ports with conflict re-pick`.

### Task 3: Change journal

**Files:**
- Create: `internal/journal/journal.go`, `internal/journal/journal_test.go`

**Interfaces:**
- Produces:
  - `type journal.Entry struct { ID string; Kind string; Target string; Prev json.RawMessage; Applied json.RawMessage; At time.Time }`.
  - `journal.Open(dir string) (*Journal, error)`.
  - `(*Journal).Begin(e Entry) error` appends the line and fsyncs the file and the dir before the caller applies the change.
  - `(*Journal).Done(id string) error` marks it reverted.
  - `(*Journal).Pending() ([]Entry, error)`.
  - `type journal.Reverter interface { Current(target string) (json.RawMessage, error); Restore(target string, prev json.RawMessage) error }`.
  - `(*Journal).RevertAll(rs map[string]Reverter) []RevertResult`, with `RevertResult{Entry; Outcome; Err}` and Outcome `Restored|SkippedChangedByOther|Failed`.
- Rule: revert restores `Prev` only if `Current == Applied`. Otherwise the outcome is `SkippedChangedByOther` and the entry is closed (Review Focus 2). Reverting runs newest first.

- [ ] **Step 1: Failing tests** (fake Reverter over a map):
  - `TestRevertRestoresPrev`.
  - `TestRevertSkipsChangedByOther`: Current differs from Applied, so nothing is written and the outcome is `SkippedChangedByOther`.
  - `TestRevertNewestFirst`: two entries on one target restore the oldest Prev.
  - `TestTornLastLine`: a truncated final line (crash mid-append) is ignored. Earlier entries still revert.
  - `TestBeginFsyncsBeforeReturn`: an unexported sync hook is called before Begin returns.
  - `TestDoneIsIdempotent`.
  - `TestCompaction`: after RevertAll, with all entries done, the file is rewritten to empty atomically and size stays flat over 1000 cycles.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** (JSON lines; `Done` appends a `{"done":id}` line). **Step 4: Run** → PASS. **Commit** `feat(journal): fsynced change journal with safe revert`.

### Task 4: Windows system proxy

**Files:**
- Create: `internal/sysproxy/sysproxy.go` (conflict logic, OS-independent), `internal/sysproxy/winreg_windows.go` (live), `internal/sysproxy/sysproxy_test.go`, `internal/sysproxy/sysproxy_systemtest_test.go` (`systemtest`, windows)
- Modify: `internal/sysapi/sysapi.go` (add `Connections() ([]string, error)` and per-connection `GetConn/SetConn` to `SystemProxy`, and the same to `Recorder`)

**Interfaces:**
- Consumes: `sysapi.SystemProxy`, `sysapi.ProxyState`, `journal`.
- Produces:
  - `sysproxy.Manager` with `New(sp sysapi.SystemProxy, j *journal.Journal) *Manager`.
  - `(*Manager).Apply(ours sysapi.ProxyState, force bool) error` journals the previous state of the LAN settings and every RAS entry first, then sets them. It returns `*ConflictError{Current}` when the current state is a foreign proxy or PAC and `force` is false.
  - `(*Manager).Revert() error`.
  - `(*Manager).Drifted() (bool, sysapi.ProxyState, error)`: true when the current state is not ours.
  - `sysproxy.Reverter()` is the `journal.Reverter` for kind `"sysproxy"`.
  - `sysproxy.NewWindows() sysapi.SystemProxy` (live). Ours = `Server: "127.0.0.1:<Mixed>"` and `Bypass: ["<local>", "localhost", "127.*", "10.*", "172.16.*"…"172.31.*", "192.168.*"]`.
- Live implementation: read and write `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings` (`ProxyEnable`, `ProxyServer`, `ProxyOverride`, `AutoConfigURL`) through `InternetSetOptionW(INTERNET_OPTION_PER_CONNECTION_OPTION)` for the LAN connection (nil) and each RAS entry from `RasEnumEntriesW`, then `INTERNET_OPTION_SETTINGS_CHANGED` + `INTERNET_OPTION_REFRESH`. No `netsh`, no `reg.exe`.

- [ ] **Step 1: Failing unit tests** (Recorder / fake SystemProxy):
  - `TestApplyJournalsBeforeSet`: the fake records call order Begin → Set.
  - `TestForeignPACConflict`: a current `PACURL` (or a non-ours `Server`) gives `*ConflictError` with nothing set. With force=true it applies, and Revert restores the PAC.
  - `TestRevertRestoresExactPrev`: bypass list order and PAC are restored byte-equal (Review Focus 1).
  - `TestDriftDetected` and `TestRevertAfterDriftKeepsTheirs`: another writer changes the proxy, so Revert leaves it and the outcome is `SkippedChangedByOther` (Review Focus 2).
  - `TestRASEntriesCovered`: two fake RAS entries are each journaled and restored.
- [ ] **Step 2: Systemtest** (`windows`, `systemtest`): `TestLiveApplyRevert`.
  - Save the runner's state.
  - Apply ours, then read it back through WinINet `InternetQueryOptionW` and the registry.
  - Revert and assert the saved state is back.
  - The `t.Cleanup` restore also runs on failure.
- [ ] **Step 3: Run** → FAIL. **Step 4: Implement.** **Step 5: Run** unit tests → PASS. Add `./internal/sysproxy/...` to the windows `systemtest-windows` pkgs in ci.yml, push, check the CI windows job is green. **Commit** `feat(sysproxy): journaled Windows system proxy with conflict detection`.

### Task 5: Windows key providers

**Files:**
- Create: `internal/store/keys_windows.go`, `internal/store/keys_windows_test.go`, `internal/store/keys_systemtest_test.go` (`systemtest`)

**Interfaces:**
- Consumes: `store.KeyProvider`, `store.ErrKey`.
- Produces:
  - `store.DPAPIFileKey(path string) KeyProvider`: a random 32-byte key, sealed with `CryptProtectData` (CurrentUser scope, entropy `"zatvor-store-v1"`), in a user-only file. This has no system side effects, so it is used in safe mode and tests.
  - `store.CredManKey(target string) KeyProvider`: a generic credential `Zatvor/<target>` (`CredWriteW`, CRED_PERSIST_LOCAL_MACHINE), used in live mode (spec §5.6).
  - Both return `store.ErrKey` wrapping the cause, never the key bytes.
- Rule: Credential Manager is a user setting forbidden on Artur's PC, so the daemon uses `CredManKey` only in live mode (Task 11). Migrating from a DPAPI key to CredMan happens on first live start: read the store with the old key, save it with the new one, journal it.

- [ ] **Step 1: Failing tests** (windows unit, no system change): `TestDPAPIRoundTrip` (same key across two providers on one file); `TestDPAPIFileUnreadableByOthers` (owner-only DACL); `TestDPAPITamperedFile` (a flipped byte gives `ErrKey`). Systemtest `TestCredManRoundTrip` creates and reads the key, deletes it in Cleanup, and checks a second `Key()` returns the same bytes.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS; add `./internal/store/...` to the windows systemtest pkgs. **Commit** `feat(store): DPAPI and Credential Manager key providers`.

### Task 6: Geo assets: bundled pin, cache, merged sets, profile URLs

**Files:**
- Create: `third_party/geo/pin.json`, `tools/geofetch/main.go`, `internal/geo/assets/assets.go`, `internal/geo/assets/assets_test.go`
- Modify: `NOTICE` (versions), `.gitignore` (`third_party/geo/files/`), `.github/workflows/ci.yml` (cache + fetch step for jobs that need real files)

**Interfaces:**
- Consumes: `merge.Merge`, `merge.Source`, `merge.MissingTagError`, `geo.NeededTags`, `subfetch.Dialer`.
- Produces:
  - `third_party/geo/pin.json` = `{"roscomvpn":{"tag":…,"geoip":{"url","sha256"},"geosite":{"url","sha256"}},"v2fly":{…}}`. The pinned tags are the latest releases at implementation time. Each SHA-256 is cross-checked against the publisher's `.sha256` file where one exists.
  - `go run ./tools/geofetch -dest third_party/geo/files` verifies every hash and refuses a mismatch.
  - `assets.New(bundledDir, cacheDir, mergedRoot string) *Assets`.
  - `(*Assets).Prepare(cfg []byte, profileGeo *ProfileGeo) (assetDir string, missing []merge.MissingTagError, err error)`. It merges into a fresh dir `mergedRoot/<sha256 of sorted tags + inputs>` and reuses an existing complete one. A marker file is written last, so a dir is complete only with its marker. Old dirs are removed, keeping the last 2. This fixes the 1a two-rename window.
  - `type ProfileGeo struct { GeoIPURL, GeoSiteURL string }` comes from Happ `Geoipurl`/`Geositeurl` (spec §6.5).
  - `(*Assets).Fetch(ctx, pg ProfileGeo, dial subfetch.Dialer) error` downloads through the tunnel dialer, verifies `<url>.sha256` when served, swaps the cache atomically, and is a no-op when the URL or SHA-256 equals a bundled file.
  - `(*Assets).NeedsBootstrap(cfg, pg) bool` is true when tags are missing from bundled + cache but the profile names geo URLs (Q2.10).

- [ ] **Step 1: Failing tests** (synthetic dat via `synth.Write`, `httptest` server as the geo URL):
  - `TestPrepareReusesCompleteDir`.
  - `TestPrepareIgnoresDirWithoutMarker`.
  - `TestKeepsLastTwo`.
  - `TestFetchVerifiesSHA256`: a bad hash leaves the cache untouched and returns an error.
  - `TestFetchSkipsWhenBundledMatches`: zero requests to the server.
  - `TestFetchUsesGivenDialer`: the dialer is called; there is no direct dial.
  - `TestNeedsBootstrap`: a profile tag absent from bundled with a geo URL gives true; with no URL it gives false and `missing` names the tag.
  - `TestNoAutoUpdateWithoutCall`: Assets never opens a connection by itself (pitfall 56).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement**; run `tools/geofetch` once and record the pin. **Step 4: Run** → PASS. **Commit** `feat(geo): pinned bundled geo, profile geo URLs and merged-set cache`.

### Task 7: Connection engine

**Files:**
- Create: `internal/engine/engine.go`, `internal/engine/tunnelcheck.go`, `internal/engine/engine_test.go`
- Modify: `internal/xrayrun/run.go` + new `internal/xrayrun/job_windows.go` (assign the child to a job object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, so Xray dies with the daemon); `internal/xrayrun/latency.go` (when the shared run fails to start, fall back to one run per outbound)

**Interfaces:**
- Consumes: `gen.Build`, `gen.Input`, `validate.Schema`, `validate.XrayTest`, `xrayrun.Runner`, `assets.Assets`, `sysproxy.Manager`, `ports.Pick`, `settings`.
- Produces:
  - `engine.New(cfg engine.Deps) *Engine`. `Deps` holds the xray path, assets, the proxy manager, a ports func, a clock, a `Checker` and the safe flag.
  - `(*Engine).Connect(ctx, p profile.Profile) error`. The steps are: pick ports, Build (with `BootstrapAllProxy` when `NeedsBootstrap`), Prepare geo, Schema plus XrayTest, Start, ready, the tunnel check, and finally `sysproxy.Apply`. The system proxy is set only after the check passes.
  - `(*Engine).Disconnect(ctx) error` reverts the proxy, then stops Xray.
  - `(*Engine).State() engine.State` = `{Phase: Disconnected|Connecting|Connected|Degraded|Failed; Profile string; Ports gen.Ports; Check CheckResult; Err *engine.Error}`.
  - `(*Engine).Events() <-chan State`.
  - `engine.TunnelCheck(ctx, dial subfetch.Dialer, services []string) CheckResult` = `{Status: OK|Bad|Unknown; IPv4 string; At time.Time; Gen uint64}`. It runs a GET through the authenticated probe inbound against at least 2 of `https://api.ipify.org`, `https://ifconfig.co/ip`, `https://icanhazip.com` (none in `.ru`, R3.10). The result is OK only when 2 answers agree, and Unknown when fewer than 2 answer. It never maps unknown to OK (K13).
  - Fail policy (§7.7): Xray `Exited`/`GaveUp` → Phase Degraded and `sysproxy.Revert()` immediately, so the user's internet works without us. `Restarting` then healthy → re-check, then re-Apply. Drift → warning in State, nothing overwritten.
  - Geo bootstrap: when connected with `BootstrapAllProxy`, run `assets.Fetch` through the probe dialer, then rebuild without bootstrap and hot-restart.

- [ ] **Step 1: Failing tests** (fake runner via the Task 10 1a helper pattern, fake proxy manager, fake checker, fake clock):
  - `TestProxySetOnlyAfterCheck`.
  - `TestCheckUnknownMeansNoProxy`: Phase Failed, Err code `tunnel_check_unknown`, and no Apply.
  - `TestXrayCrashRevertsProxy` then `TestRecoveryReapplies`.
  - `TestPortConflictRepicks`: the persisted Mixed port is busy, so a new port is used in both the config and the proxy (Review Focus 5).
  - `TestDriftWarnsNoOverwrite`.
  - `TestBootstrapThenSwitch`: the first config has the bootstrap rule; after Fetch the second does not; Fetch got the probe dialer.
  - `TestDisconnectOrder`: Revert happens before Stop.
  - `TestTunnelCheckAgreement`: two agree → OK, one answers → Unknown, two disagree → Bad.
  - Windows-only `TestJobObjectKillsXray`: kill the parent helper process and the fake xray child exits (testguard-spawned).
  - `TestLatencyFallbackPerOutbound`: one invalid outbound still gives RTTs for the others.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS (also `-race`). **Commit** `feat(engine): connect through system proxy with tunnel check and fail policy`.

### Task 8: Subscription service and scheduler

**Files:**
- Create: `internal/subsvc/subsvc.go`, `internal/subsvc/schedule.go`, `internal/subsvc/subsvc_test.go`

**Interfaces:**
- Consumes: `subfetch.Fetcher`, `subfetch.Request`, `hwid.Headers`, `hwid.MachineID`, `profile.Apply`, `store.Store`, `validate.Schema`, `gen.Build` (to validate each new profile builds), `engine` (for state and the probe dialer).
- Produces:
  - `subsvc.New(d subsvc.Deps) *Service`.
  - `(*Service).Add(ctx, url, ua string) (profile.Subscription, profile.Diff, error)` creates a subscription with `SendDevice: true`.
  - `(*Service).Update(ctx, id string, trigger Trigger) (profile.Diff, error)` with `Trigger` = `User|Scheduled|Start`.
  - `(*Service).Remove(id string) error`.
  - `(*Service).SetUA(id, ua string) error`.
  - `(*Service).List() []profile.Subscription`, with each URL redacted to its host in the API view.
  - `(*Service).Run(ctx)`: the scheduler loop.
  - `subsvc.Error{Code}` with codes `no_tunnel`, `device_limit`, `hwid_required`, `encrypted`, `clash_unsupported`, `not_subscription`, `empty`, `network`, `http_status`, `invalid_profiles`.
- Rules:
  - Fetch through `engine` probe dialer when Connected, else direct.
  - `Scheduled` with no tunnel → skip, no request (R3.6).
  - Backoff for scheduled failures 1, 2, 4, 8… minutes, capped at `Info.UpdateInterval`, default 24 h.
  - Each new profile must pass `gen.Build` + `validate.Schema` before Apply's result is saved. Profiles that fail are dropped with a Diff warning, and the update fails if none survive (spec §5.6).
  - `http://` URL → no HWID or device headers; Diff gets a `plain_http` warning.
  - Concurrent `Update` for one id = one request (subfetch singleflight).

- [ ] **Step 1: Failing tests** (httptest provider counting requests, fake engine, fake clock):
  - `TestAddDefaultsSendDevice`.
  - `TestScheduledSkipsWithoutTunnel`: 0 requests (Review Focus 4).
  - `TestUserUpdateWithoutTunnelIsOneDirectRequest`.
  - `TestScheduledBackoffCapped`: failure times give delays 1, 2, 4, 8 min and then the cap.
  - `TestDeviceLimitKeepsProfiles`.
  - `TestInvalidProfileDropped`.
  - `TestPlainHTTPNoHWID`.
  - `TestThroughTunnelWhenConnected`: the provider sees the request arrive via the fake dialer.
  - `TestNoURLInErrorsOrList`.
  - `TestStoreSavedOnlyOnSuccess`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(subsvc): subscription service with tunnel-only scheduled updates`.

### Task 9: Developer-tool routing

**Files:**
- Create: `internal/devtools/devtools.go`, `internal/devtools/shell.go`, `internal/devtools/jsonfile.go`, `internal/devtools/wrappers.go`, `internal/devtools/devtools_test.go`, `testdata/devtools/*` (fixture files with CRLF, BOM, comments)
- Modify: `go.mod` (`github.com/tailscale/hujson`), `NOTICE`

**Interfaces:**
- Consumes: `journal`, `gen.Ports.DevTools`.
- Produces:
  - `devtools.New(roots devtools.Roots, j *journal.Journal, fs devtools.FS) *Manager`. `Roots` = `{Home, Documents, AppData, StateFile, BinDir string}`; all paths are derived from roots, never from the real home in tests. `FS` is a small interface (read, stat, atomic write through a symlink) so safe mode can pass a recorder.
  - `(*Manager).Enable(port int) error`, `(*Manager).Disable() error`.
  - `(*Manager).Status() []Target`, where `Target` = `{Name, Path, State: On|Off|Conflict|Missing, Note}`.
  - `devtools.Reverter()` is the reverter for kind `"devtools"`.
- Targets:
  - **PowerShell 5.1 and 7.** Profiles: `Documents\WindowsPowerShell\Microsoft.PowerShell_profile.ps1` and `Documents\PowerShell\Microsoft.PowerShell_profile.ps1`. The block is marked `# >>> zatvor >>>` … `# <<< zatvor <<<`. It wraps `prompt` so the shell re-reads `StateFile` when its mtime changes, then sets or clears `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY=localhost,127.0.0.1,::1`.
  - **Git Bash.** `~/.bashrc` gets the same block through `PROMPT_COMMAND`.
  - **Claude Code.** `~/.claude/settings.json` gets `env.HTTPS_PROXY` and `env.NO_PROXY` keys only.
  - **VS Code.** `%APPDATA%\Code\User\settings.json` gets `http.proxy` and `http.proxySupport: "override"` only.
  - **codex and gemini.** `.cmd` and `.ps1` wrappers go in `BinDir`, which is ours and is not added to PATH automatically; the API reports the path. Each wrapper sets the env and execs the real tool found on PATH, excluding BinDir.
  - **Codex `respect_system_proxy`.** Detected in `~/.codex/config.toml`; reported as a `Note`, never edited.
  - **cmd.exe.** No profile mechanism without AutoRun registry hooks. Not in 1b; the wrapper covers codex/gemini in cmd.
- StateFile holds `http://127.0.0.1:<DevTools>` when connected and is empty when not. The engine writes it on connect and disconnect. Open terminals switch at their next prompt.

- [ ] **Step 1: Failing tests** (temp roots):
  - `TestBlockInsertedOnceAndRemovedExactly`: Enable twice gives one block; Disable restores the file byte-equal, CRLF and BOM included.
  - `TestWritesThroughSymlink`: the profile is a symlink and stays one; the target changes (Review Focus 3).
  - `TestJSONCCommentsKept`: VS Code settings with comments and trailing commas keep everything except our two keys.
  - `TestRaceRetryNeverClobbers`: a hook changes the file between read and rename; the retry merges, and after 3 losses the result is `Conflict` and the other writer's bytes are intact.
  - `TestOnlyOurKeysReverted`: the user edits another key while enabled, and Disable keeps the user's edit.
  - `TestStateFileSwitch`: the generated PowerShell block text contains the mtime check (golden), and a bash block golden.
  - `TestCodexRespectSystemProxyNote`.
  - `TestWrapperFindsRealToolNotItself`.
  - `TestJournaledBeforeWrite`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(devtools): journaled proxy routing for shells, Claude Code, VS Code and CLIs`.

### Task 10: Local API and single instance

**Files:**
- Create: `internal/api/server.go`, `internal/api/routes.go`, `internal/api/sse.go`, `internal/api/api_test.go`, `internal/instance/instance.go`, `internal/instance/mutex_windows.go`, `internal/instance/lock_unix.go`, `internal/instance/instance_test.go`
- Modify: `docs/decisions/0009-local-api.md` (settle: HTTP only in 1b; named pipe reconsidered in 1c)

**Interfaces:**
- Consumes: `engine.Engine`, `subsvc.Service`, `devtools.Manager`, `settings`, `diag` (Task 12 adds the route).
- Produces:
  - `api.New(b api.Backend, dirs appdir.Dirs) *Server`. `Backend` is an interface over the services, so tests use fakes.
  - `(*Server).Start(ctx) (addr string, err error)` binds `127.0.0.1:0` (or the persisted port when free), writes `Run/api.json` = `{"version":1,"port":N,"pid":P}` and `Run/token` (32 random bytes hex, user-only). `(*Server).Stop()` removes both.
  - Routes under `/v1/`:
    - `GET status`, `GET events` (SSE: `state`, `subscription`, `warning`).
    - `GET profiles`, `POST connect {profile}`, `POST disconnect`.
    - `GET subscriptions`, `POST subscriptions {url, ua}`, `POST subscriptions/{id}/update`, `DELETE subscriptions/{id}`.
    - `PUT settings`, `POST devtools {on}`, `POST proxy/resolve-conflict {force}`.
    - `POST show` and `GET diag` (Task 12).
  - Every response is JSON, and errors are `{"code","message"}`.
  - `instance.Acquire(name string) (release func(), already bool, err error)` uses the named mutex `Local\Zatvor-<user SID>` on Windows and a `flock` on the run dir elsewhere. `instance.Forward(dirs) error` POSTs `/v1/show` using the discovery file (K43).
- Checks on every request: `Authorization: Bearer <token>` (constant-time compare), `Host == 127.0.0.1:<port>`, and `Origin` empty or `null`. 1c adds its own origin. On failure → 403 `{"code":"forbidden"}` with no detail. Bodies are capped at 64 KiB.

- [ ] **Step 1: Failing tests** (`httptest`-free: start the real server on 127.0.0.1:0 with a fake Backend):
  - `TestRejectsMissingToken`, `TestRejectsWrongHost` (`Host: localhost:N`, `Host: evil.example:N` → 403; DNS rebinding).
  - `TestRejectsForeignOrigin`.
  - `TestTokenFileUserOnly`, `TestDiscoveryRemovedOnStop`.
  - `TestSSEDeliversStateChange`.
  - `TestErrorBodiesCarryNoSecret`: the backend returns an error that wraps a URL, and the body holds only code and message without the URL.
  - `TestSubscriptionsListRedacted`.
  - `TestBodyLimit`.
  - `TestSecondInstanceForwardsShow`: two `Acquire` calls → `already=true`; Forward reaches a fake server's `/v1/show`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(api,instance): local API v1 with token, Host/Origin checks and single instance`.

### Task 11: Daemon assembly and supervisor

**Files:**
- Create: `internal/daemon/daemon.go`, `internal/daemon/daemon_test.go`, `internal/supervisor/supervisor.go`, `internal/supervisor/supervisor_test.go`
- Modify: `cmd/zatvord/main.go`, `cmd/zatvord/main_test.go`

**Interfaces:**
- Consumes: everything above; `safemode.Resolve`; `sysapi.Recorder`.
- Produces:
  - `daemon.Run(ctx, opts daemon.Options) error`. `Options` = `{Dirs appdir.Dirs; Mode safemode.Mode; XrayPath, BundledGeo string; Version string; Clock}`.
  - Start order:
    1. `instance.Acquire`; if another instance runs, `Forward` and exit 0.
    2. `journal.RevertAll` with the sysproxy and devtools reverters, results logged with codes.
    3. Settings, then the store (`DPAPIFileKey` in safe mode, `CredManKey` + one-time migration in live mode), services, API.
    4. If `UpdateOnStart`, one direct update per subscription only when the user enabled it. The default is off.
  - Exit: Disconnect, devtools Disable if it was enabled for this session only (setting), `RevertAll`, API Stop.
  - Safe mode: `sysapi.Recorder` writes to `Logs/safe-mode.jsonl`, and devtools gets a recorder FS, so no file outside `Dirs` is touched.
  - `supervisor.Run(ctx, child func() *exec.Cmd, j *journal.Journal, rs map[string]journal.Reverter, clock) error` restarts the daemon with backoff 1, 2, 4, 8, 16 s. It gives up after 5 crashes in 2 minutes, then runs `RevertAll` so the system proxy is restored when the daemon cannot come back (spec §8).
  - `cmd/zatvord`: flags `--data-dir`, `--safe`, `--supervised` (child mode), `--version`, `--print-mode`. Without `--supervised`, the binary acts as the supervisor and starts itself with `--supervised`.

- [ ] **Step 1: Failing tests:**
  - `TestStartRevertsPendingJournal`: a pre-written journal entry for a fake sysproxy is restored before the API starts.
  - `TestSafeModeTouchesNothingOutsideDirs`: run the daemon in safe mode with temp roots and devtools enabled; the only files written are under Dirs; `safe-mode.jsonl` has the sysproxy Set.
  - `TestExitRevertsAll`.
  - `TestSecondStartForwards`.
  - `TestSupervisorRestarts`: a fake child crashes 3 times, then runs.
  - `TestSupervisorGivesUpAndReverts`: 5 crashes in 2 min → RevertAll called and exit with a typed error.
  - `TestSupervisorDoesNotRevertOnCleanExit`.
  - `TestLiveRefusedWithoutAllowLive` (ADR 0004 gate).
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(daemon): assemble zatvord with journal revert, safe mode and supervisor`.

### Task 12: CLI and diagnostics

**Files:**
- Create: `cmd/zatvor/main.go`, `cmd/zatvor/main_test.go`, `internal/diag/diag.go`, `internal/diag/diag_test.go`
- Modify: `internal/api/routes.go` (`GET /v1/diag` streams the zip)

**Interfaces:**
- Consumes: the API (discovery + token), `validate.Scrub`.
- Produces:
  - `zatvor status [--json]` prints the phase, profile, ports, last check and warnings.
  - `zatvor check [--json]` runs a fresh tunnel check via the API. The exit codes for stage 1 are:
    - 0: connected and the tunnel check is OK.
    - 1: not connected, or the check is Bad (traffic does not go through Zatvor).
    - 2: unknown (the check could not run).
    - 3: the daemon is not running.

    Stage 2 extends 1 to "address not allowed" (spec §10).
  - `zatvor diag -o <file.zip>`.
  - `diag.Bundle(w io.Writer, src diag.Sources) error` packs: versions (zatvord, Xray pin, OS build), settings, journal, the last 200 lines of the daemon log, the last Xray stderr ring (already scrubbed), State, and the subscription list (hosts only).
  - Before writing, it runs a redaction pass: every secret value from the store (URLs, ids, passwords, keys) is replaced by `***`, using the same variants as `validate.Scrub`.

- [ ] **Step 1: Failing tests:**
  - `TestCheckExitCodes`: a fake API server serves each state → 0/1/2. With no discovery file → 3.
  - `TestStatusJSONStable`: a golden JSON shape.
  - `TestDiagRedactsEverySecret`: seed the store with an awkward URL token, a uuid, a password with `"\#%` and an emoji, then scan every byte of the unzipped bundle; none appears in any form.
  - `TestDiagNoFilesOutsideList`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS. **Commit** `feat(cli,diag): status, check with exit codes, redacted diagnostics`.

### Task 13: Live acceptance on the Windows runner

**Files:**
- Create: `internal/daemon/daemon_systemtest_test.go` (`systemtest`, windows), `internal/daemon/testserver.go` (reuses `internal/e2e` server helpers: move the shared parts to `internal/e2e/servers.go` exports if needed)
- Modify: `.github/workflows/ci.yml` (windows systemtest job: fetch the Xray pin and geo pin, set `ZATVOR_XRAY`, add `./internal/daemon/...`)

**Interfaces:**
- Consumes: `daemon.Run` in live mode, the local Hysteria2 and VLESS servers from `internal/e2e`, and an `httptest` subscription server serving a links body that points at them.

- [ ] **Step 1: Systemtest `TestLiveDaemonEndToEnd`**, in a temp data dir:
  1. Start the daemon in live mode.
  2. Add the subscription through the API.
  3. Connect the VLESS Reality profile.
  4. Assert the registry proxy = `127.0.0.1:<Mixed>`.
  5. Send an HTTP GET via a WinINet-free Go client that reads the registry proxy, and check the echo server sees the Reality server's path (its access log).
  6. Run `zatvor check` → exit 0.
  7. Disconnect and assert the runner's original proxy state is back.
- [ ] **Step 2: Systemtest `TestLiveCrashRevert`** (Review Focus 1):
  1. Set a foreign PAC on the runner first, then connect with force=true.
  2. Kill the daemon child with `TerminateProcess` by our own handle.
  3. The supervisor restarts it, and the restart's `RevertAll` restores the PAC exactly.
  4. Variant: kill the supervisor and the daemon both, then a fresh start reverts.
- [ ] **Step 3: Systemtest `TestLiveDevToolsOnRunner`.** Enable on the runner's real profile files (disposable runner). Open `powershell -NoProfile -Command ". $PROFILE; prompt; $env:HTTPS_PROXY"` and see our proxy. Disable, and the profile bytes equal the saved copy.
- [ ] **Step 4: Run in CI** → FAIL first (missing wiring), then PASS. Windows systemtest job green twice in a row. **Commit** `test(daemon): live Windows acceptance: connect, crash revert, dev tools`.

### Task 14: Docs and stage 1b wrap-up

**Files:**
- Modify: `docs/STATUS.md`, `CHANGELOG.md`, `README.md` + `README.en.md` ("What Zatvor sends": the tunnel-check services and their rate, on connect and on `zatvor check` only in 1b), `docs/decisions/README.md` (+ ADR 0012 "journal and revert semantics" and ADR 0013 "dev-tool routing targets", short)
- Create: `docs/checklists/stage-1b.md`

- [ ] **Step 1:** STATUS records 1b as done, 1c next, and the deferred items. CHANGELOG per package. The checklist for Artur: nothing to run on his PC; it links the green `systemtest-windows` runs with the three live tests and says what each proves.
- [ ] **Step 2: Commit** `docs: stage 1b status, changelog and checklist`.

---

## Next plan (outline only, written after 1b)

- **1c UI and delivery:**
  - The Tauri tray and popup on the local API: styles as classes, the origin added to the API allow-list, and a named pipe reconsidered.
  - Panel basics: overview, profiles and subscriptions, settings, about.
  - First run: paste the link, then a warning that the first request registers a device, then connect.
  - The "send device info" switch, the Happ hint card (§5.3) and the update diff.
  - ru/en and light/dark.
  - MSI (WiX v5), machine-wide.
  - Stage 1 acceptance with Artur: his test subscription imports, and Hysteria2-DNS, XHTTP and Vision connect from his home internet.
