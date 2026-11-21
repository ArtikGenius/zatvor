# Stage 0c: what to look at

All measurements ran on GitHub Actions (Windows Sandbox was replaced by CI Windows runners).

## CI evidence (branch `stage-0c`)

| Workflow | Latest green run |
|---|---|
| ci.yml (build, tests, secret scan, Xray integration) | [44945859285](https://github.com/ArtikGenius/zatvor/actions/runs/44945859285) |
| proto-tun.yml (TUN A vs B on Windows) | [44926571615](https://github.com/ArtikGenius/zatvor/actions/runs/44926571615) |
| proto-ui.yml (Tauri vs Wails, Windows and macOS) | [44943576646](https://github.com/ArtikGenius/zatvor/actions/runs/44943576646) |
| proto-installer.yml (WiX vs Inno) | [44945859245](https://github.com/ArtikGenius/zatvor/actions/runs/44945859245) |

Workflows: https://github.com/ArtikGenius/zatvor/actions

## Needs you: Mac tray check

Follow "Checklist for Artur's Mac" in `proto/ui/RESULTS.md`. The Mac builds are artifacts of run
[44929300703](https://github.com/ArtikGenius/zatvor/actions/runs/44929300703). Tauri 2 is provisional until
this is done ([ADR 0005](../decisions/0005-ui-framework.md)).

## Decided (details in [docs/decisions](../decisions/README.md))

- 0001 Go daemon, separate supervisor, helper, UI, CLI.
- 0002 Xray runs as a separate supervised process.
- 0003 TUN front: Xray's `tun` inbound (A).
- 0004 Safe mode by default; tests cannot touch the real system.
- 0005 UI: Tauri 2 (provisional, Mac check).
- 0006 Installer: WiX v5 / MSI.
- 0007 Xray pinned at v26.11.9; roscomvpn-wins geo merge.
- 0008 HWID algorithm frozen forever.
- 0009 Local API: HTTP on 127.0.0.1 with token.

## Deferred

- Windows Sandbox harness (you cannot reboot now; CI runners used instead).
- Windows-taskbar tray placement check: stage 1 (no Windows device connected).
- IPv6 leak and sleep/wake for TUN: stage 3 VM.
- Full list: "Open follow-ups" in [docs/STATUS.md](../STATUS.md).
