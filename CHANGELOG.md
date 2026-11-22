# Changelog

All notable changes to Zatvor are recorded here.

## Unreleased

### Stage 1b (zatvord on Windows)

- `appdir`, `settings`: per-user data, run, log and journal directories with a single full-control ACE on Windows; versioned settings that refuse a newer file and never save an empty one; `LocalAPIPort` kept apart from the Xray ports.
- `ports`: persisted loopback ports re-validated at start; a busy or reserved port is re-picked, with a gap between ports and well-known proxy ports avoided.
- `journal`: fsynced write-ahead change journal; `RevertAll` restores a value only while it is still ours and never closes older entries behind a failed one; torn tails repaired.
- `sysproxy`: journaled Windows system proxy for the LAN and RAS connections, read back exactly (including the auto-detect flag); a foreign PAC or manual proxy is a typed conflict the user resolves.
- `store` keys: DPAPI file key (safe mode) and Credential Manager key (live mode), with the DPAPI-to-Credential-Manager migration.
- `geo/assets`, `tools/geofetch`: pinned bundled geo (git-tracked roscomvpn files, v2fly), profile geo URLs over https, merged-set cache with rollback; `geofetch` downloads and verifies the pins.
- `engine`: connect through the system proxy with the three-service tunnel check, fail policy, geo bootstrap, drift warnings and conflict resolution; superseded and cancelled connects never touch the live session.
- `xrayrun`: Windows job object kill-on-close; per-outbound latency fallback only for config failures; latency waits for our own SOCKS5 inbound.
- `subsvc`: subscription service (add, refresh, scheduled updates only through the tunnel); a changed-but-invalid profile keeps the old working copy; new subscriptions send device headers.
- `devtools`: journaled proxy routing for PowerShell, bash, Claude Code, VS Code, and Codex and Gemini wrappers with a nesting guard (ADR 0013).
- `api`, `instance`: local API v1 with token, `Host` and `Origin` checks and SSE events; single instance per user session with discovery file.
- `daemon`: `zatvord` assembled with crash recovery, per-mode stores and journals, log rotation, shutdown gate and state-file retry.
- `supervisor`: restarts a crashed daemon with backoff and reverts the journal itself when it gives up (ADR 0012).
- `cmd/zatvord`: the daemon binary; safe mode is the default, live mode needs `ZATVOR_ALLOW_LIVE=1`.
- `cmd/zatvor`: `status`, `check` (exit codes 0 ok, 1 bad or not connected, 2 unknown, 3 daemon not running) and `diag -o FILE.zip`.
- `diag`, `validate`: redacted diagnostics bundle (stored secrets, URL user info, dev-tool values and exit addresses replaced); `validate` gains secret-variant helpers for it.
- Live Windows acceptance on `windows-latest` (`systemtest`): connect, crash revert and dev tools; runs 45048050788 and 45049736536.
- ADRs 0012 and 0013; README "What Zatvor sends" updated; stage 1b checklist.
- Final fix wave: a failed start-up refresh backs off from 1 minute; protocol-aware Xray ready checks; the recovery re-check backs off up to 5 minutes; the dev-tools toggle applies at once (the dev-tools inbound is always built); discovery files are rewritten without an API restart; the supervisor's revert empties the dev-tool state file; `safe-mode.jsonl` records writes only and rotates at 5 MiB; `shutting_down` is HTTP 503; foreign PAC URL queries masked in diagnostics; the Windows FLAGS_UI fallback narrowed to `ERROR_INVALID_PARAMETER`.

### Stage 1a (core engine)

- `ojson`: JSON objects with key order kept, used for configs and the store.
- `link`: share-link parser and exporter (vless, vmess, trojan, ss, socks, hysteria2); outbound mapping checked against Xray's builder; no `allowInsecure`, pinned certificates mapped instead.
- `subbody`, `subhdr`: tolerant subscription body decoder (base64, Clash, JSON, inline headers) and header parsing.
- `hwid`: frozen HWID with host and machine id normalisation; optional device headers.
- `happ`: Happ routing profile and FakeDNS translation.
- `subfetch`: single-flight subscription fetcher; redirect policy; HWID dropped on a host change; subscription URL never in errors.
- `profile`, `store`: update merge, diff, and encrypted store with one backup that a bad file never replaces.
- `gen`: Xray config builder with rule layers and presets; provider routing never rewritten.
- `geo/merge`: merged geo files per tag set.
- `validate`: schema check and `xray -test`; config secrets scrubbed from messages.
- `xrayrun`: supervised Xray runner with backoff and a core latency test; exact liveness on Windows.
- `e2e`: end-to-end tests over Reality+Vision, XHTTP and Hysteria2 with a real Xray.
- `secretscan`: staged scan matches the full scan; fake test UUIDs allow-listed; `scripts/dev/enable-hooks.sh`.
- ADRs 0010 and 0011; README "What Zatvor sends".
- Final review fixes: no `Referer` on subscription redirects; provider `metrics` section removed with a warning; typed errors (`link.ErrMalformed`, `store.ErrWrongKey`/`ErrCorrupt`/`ErrFormat`/`ErrVersion`/`ErrKey`, `validate.SchemaError`/`XrayTestError`/`ErrXrayNotRun`, `xrayrun.Err*`); negated geo tags (`!geoip:cn`, `geoip:!cn`) and all geo rule fields collected; TCP HTTP header refused as unsupported; one bad JSON array element skipped instead of the whole body; `xrayrun.Stop` no longer orphans a newer Start; the key file is never half-written; store document v2 keeps the provider Happ profile once; README names the latency test URL.

### Stage 0c (prototypes)

- Subscription fixtures, geo tag scanner and synthetic geo files for offline tests.
- Verified Xray v26.11.9 fetch and real-binary integration tests (Linux, Windows).
- Legacy noise migration with explicit refusals.
- Safe mode, system-change recorder and test process guard; Windows system tests on CI.
- TUN prototypes measured on Windows CI; decision: Xray `tun` inbound.
- UI prototypes (Tauri 2, Wails v3); decision: Tauri 2, macOS check pending.
- Installer prototypes (WiX, Inno) and signed install scripts; decision: WiX v5 / MSI.
- Architecture decision records 0001-0009 and the stage 0c checklist.
- Repository foundation: licence, secret scan, CI.
