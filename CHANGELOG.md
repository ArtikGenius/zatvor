# Changelog

All notable changes to Zatvor are recorded here.

## Unreleased

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
