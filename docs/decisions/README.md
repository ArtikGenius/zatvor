# Architecture decision records

One file per decision: context, options, decision, consequences. Numbers are never reused.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-go-daemon-and-components.md) | Go daemon zatvord plus separate supervisor, helper, UI and CLI | accepted |
| [0002](0002-xray-separate-process.md) | Official Xray binary as a supervised child process | accepted |
| [0003](0003-tun-front.md) | TUN front: Xray's own `tun` inbound (A); sing-tun (B) kept as fallback | accepted |
| [0004](0004-safe-mode-and-test-guard.md) | Safe mode by default, recorder, test process guard | accepted |
| [0005](0005-ui-framework.md) | UI framework: Tauri 2 | accepted, macOS check pending |
| [0006](0006-installer-and-install-script.md) | Windows installer WiX v5 / MSI, signed install scripts | accepted |
| [0007](0007-xray-pin-and-geo-merge.md) | Xray pin v26.11.9, verified fetch, roscomvpn-wins geo merge | accepted |
| [0008](0008-hwid-algorithm.md) | HWID algorithm, frozen forever | accepted, frozen |
| [0009](0009-local-api.md) | Local HTTP API on 127.0.0.1 with token | accepted |
