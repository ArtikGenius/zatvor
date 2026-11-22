# Stage 1a: what to look at

Stage 1a is the OS-independent core engine. There is nothing for you to run by hand. All proof comes from CI.

## CI evidence (branch `stage-1`)

| Run | Commit | What it shows |
|---|---|---|
| [45016047924](https://github.com/ArtikGenius/zatvor/actions/runs/45016047924) | 8d079b3 | First all-green run with the e2e tests |
| [45016399057](https://github.com/ArtikGenius/zatvor/actions/runs/45016399057) | 8528d14 | Latest: also the Windows liveness fix in `xrayrun` |

Workflows: https://github.com/ArtikGenius/zatvor/actions

The `xray-integration` job runs on Linux and Windows with the pinned official Xray binary.

## What CI proves (job `xray-integration`, package `internal/e2e`)

Each test builds a client config from a subscription-style profile with `gen`, validates it, runs it under `xrayrun`, and sends a request through the local proxy to an echo server. The test passes only if the server received that request.

- `TestE2EVLESSRealityVision`: VLESS with Reality and Vision connects and carries traffic.
- `TestE2EVLESSXHTTP`: VLESS over XHTTP connects and carries traffic.
- `TestE2EHysteria2DNS`: Hysteria2 with DNS settings connects and carries traffic.
- `TestE2ELegacyRunsAsIs`: a legacy provider config with noises via `dialerProxy` runs unchanged, with the provider's own routing and DNS.

Other jobs: unit tests and `go vet` on every package, the secret scan, and the `xrayint` tests for `geo/merge`, `validate` and `xrayrun` against the real binary.

## Needs you

Nothing in 1a.

## Deferred

- 1b: zatvord on Windows (state and local API, journal and revert, system proxy, key providers, scheduled updates, geo bootstrap, supervisor, diagnostics, CLI). System changes run only in `systemtest` on CI Windows runners or on your PC in safe mode.
- 1c: tray and panel, first run, MSI. Stage 1 acceptance from your home internet: your test subscription imports; Hysteria2-DNS, XHTTP and Vision connect.
- 1b: new subscriptions are created with `SendDevice=true` (ADR 0008; the code default is false). 1b also adds the connectivity check through the tunnel that the README mentions.
- 1c: the "send device info" switch in subscription settings.
- Mac tray check and the Windows-taskbar tray check (see [docs/STATUS.md](../STATUS.md)).
- Deferred review findings: "Deferred minor review findings from 1a" in [docs/STATUS.md](../STATUS.md).

## Decided in 1a

- [0010](../decisions/0010-provider-routing-and-proxy-tag.md) Provider routing is never rewritten; ProxyTag rule.
- [0011](../decisions/0011-store-format-and-backup.md) Encrypted store format and backup.
- HWID normalisation rules: [0008](../decisions/0008-hwid-algorithm.md).

## Reminder

Run `scripts/dev/enable-hooks.sh` once in every new clone, so the secret scan runs before each commit.
