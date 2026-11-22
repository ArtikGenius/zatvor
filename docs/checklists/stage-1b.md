# Stage 1b: what to look at

Stage 1b is the Windows daemon `zatvord` and the `zatvor` command line. There is nothing for you to run on your PC. All proof comes from CI on GitHub Windows runners, and nothing in 1b changes your system.

## CI evidence (branch `stage-1b`, job `systemtest-windows`)

| Run | Commit | What it shows |
|---|---|---|
| [45048050788](https://github.com/ArtikGenius/zatvor/actions/runs/45048050788) | 9d97c48 | The three live tests pass, attempts 1 and 2 |
| [45049736536](https://github.com/ArtikGenius/zatvor/actions/runs/45049736536) | 6c0c2c9 | The same after review fixes (parallel packages, a realistic shell profile, a deterministic crash case), attempts 1 and 2 |

Workflows: https://github.com/ArtikGenius/zatvor/actions

The runner is a disposable Windows machine, so the tests change its real system proxy and its real profile files. Traffic goes to local test servers, not to the internet.

## What the three live tests prove

- `TestLiveDaemonEndToEnd`: the real daemon adds a subscription through the local API, connects over Reality with a real Xray, and sets the Windows system proxy to its loopback port. A request sent through the proxy from the registry reaches the test server through the tunnel, `zatvor check` passes, and on disconnect the runner's proxy state is back as it was.
- `TestLiveCrashRevert`: the daemon is killed hard while our proxy sits over a foreign PAC script. The supervisor restarts it, and the restart puts the PAC back exactly, before the API opens. This is the crash recovery promise of the spec.
- `TestLiveDevToolsOnRunner`: with dev-tool routing on, a realistic PowerShell profile (BOM, CRLF, the user's own prompt) and `.bashrc` show the user's prompt and our proxy. After turning it off every file we may edit is byte-equal to its copy from before, and the `.zatvor.bak` copies are restored too.

Other jobs: unit tests and `go vet` on every package on Linux, macOS and Windows, the secret scan, and the Xray integration tests.

## Needs you

Nothing in 1b.

## Deferred

- 1c: tray and panel, first run, MSI, autostart. Stage 1 acceptance from your home internet: your test subscription imports; Hysteria2-DNS, XHTTP and Vision connect.
- The "send device info" switch and the update diff in the panel (1c).
- Known limits and deferred review findings: [docs/STATUS.md](../STATUS.md).

## Decided in 1b

- [0012](../decisions/0012-journal-and-revert.md) Journal and revert semantics.
- [0013](../decisions/0013-devtool-routing-targets.md) Dev-tool routing targets.

## Reminder

Run `scripts/dev/enable-hooks.sh` once in every new clone, so the secret scan runs before each commit.
