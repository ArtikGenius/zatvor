# Known pitfalls: bugs Zatvor must not repeat

Stage 0a draft, 2026-11-19. Sources: Kordon CHANGELOG (section 0) and the most-discussed closed bugs of v2rayN, Throne, Hiddify and Clash Verge Rev (sections 1-3). Every item gets a test or a documented manual check (brief section 6, 10).

## 0. Kordon (ef1m03/kordon) CHANGELOG 1.0.0–1.5.4

Source: `CHANGELOG.md` at ddf1cd7 (v1.5.4, 2026-11-18). Each entry: failure → Zatvor rule/test. "K x.y.z" is the version that fixed it. The GPL code at e01c7c4 predates every fix from 1.3.1 on, so anything borrowed from it must be checked against this list.

### 0.1 Freezing and process identity
| # | Failure | Zatvor rule / test |
|---|---|---|
| K1 | Kordon froze its own panel (an Edge/WebView2 window) when the user protected Edge (K 1.3.0, 1.4.7). | Never freeze our own process tree (UI, WebView2 children, daemon, Xray, supervisor). Exclusion by process identity, not by name. Test: protect "msedge" with our WebView2 running; our UI PIDs untouched. |
| K2 | Unfreeze/close could hit a different process after PID reuse (K 1.3.0). | Identity = PID + start time; hold a handle on Windows, pidfd on Linux; re-verify before every action. Test with fake process table that reuses PIDs. |
| K3 | After several crashes in a row, apps (including the browser) stayed frozen until reboot (K 1.4.7). | Persist a freeze journal; on start, release accumulated freezes only after a fresh check; supervisor that gives up must release (or close, by policy) everything. Test: crash loop ×N leaves zero frozen processes. |
| K4 | Terminal agents after SIGCONT went to background and got mouse garbage in the input line (K 1.4.7). | Handle TTY job control: resume the foreground process group correctly, restore terminal mode; test with a pty harness on macOS/Linux. |
| K5 | Child-process exclusion was too broad, leaving parts of a protected tree running (K 1.5.1). | Tree walk is exhaustive; exclusions are explicit and minimal; test on synthetic trees. |
| K6 | Processes launched while blocked could run (K 1.0 design). | New matching processes are frozen on detection; detection mechanism and max gap documented in the spec. |
| K7 | Freeze counted when no protected app was running; history said "Closed: -" (K 1.4.4, 1.5.4). | Only count/announce freezes that touched a process; never print empty lists. |

### 0.2 State machine
| # | Failure | Zatvor rule / test |
|---|---|---|
| K8 | Stuck forever in "waiting for a fresh route check" after VPN off/on (K 1.5.2). | Every waiting state has an exit trigger and a timeout; property test on the automaton: no reachable state without an outgoing transition on a fresh probe. |
| K9 | One service's route check kept other apps paused (K 1.5.2). | Per-group state; a group only waits on checks relevant to its own services; closed apps' checks don't block others. |
| K10 | Closing apps after grace was removed (1.5.0) then restored (1.5.2) because unfreezing let them hit the network with the wrong IP. | Default: close while still frozen. Polite close only when that app's network is blocked (TUN/WFP). |
| K11 | Apps resumed early after sleep, a Kordon crash, settings change, or a second IPv6 address (K 1.5.0). | Pause before sleep; no resume without a probe from the current network generation; config reload never resumes by itself. |
| K12 | A leak found while a check service was unreachable got forgotten (K 1.5.0). | Unknown ≠ OK. A pending leak stays until a positive check clears it. |
| K13 | Status showed green when the check could not run (K 1.5.0). | Tri-state results everywhere: ok / bad / unknown; UI never maps unknown to green. |
| K14 | Answer from the previous network released the pause after reconnect (K 1.4.3). | Network generation counter stamped on every probe; stale results dropped. |
| K15 | A slower disagreeing IP answer was ignored once two fast ones agreed (K 1.4.3). | Any answer outside the allowed set, however late within the probe window, is a possible leak. |
| K16 | New IPv6 address only caught after the full probe (K 1.4.3). | IPv6 handled independently; a new unallowed IPv6 triggers immediately. |
| K17 | VPN with several exit IPs in one country paused apps every few minutes (K 1.4.6). | Answers inside one allowed country count as agreement. |
| K18 | Slow DNS caused false "no internet" (K 1.4.4). | Retry once after ~2 s before declaring offline. |

### 0.3 Allow-list
| # | Failure | Zatvor rule / test |
|---|---|---|
| K19 | Empty allow-list refilled itself with the current (home) IP (K 1.4.1). | Never auto-fill. Suggestions only, user confirms. Home IP never auto-allowed. |
| K20 | IPv6 allowed together with IPv4 automatically (K 1.4.3). | IPv6 confirmed separately. |
| K21 | Adding IP/country without seeing the consequence (K 1.4.3). | Confirmation shows address/country and effect; last local change undoable. |
| K22 | Trusted network one-click in tray disabled protection at home by accident (K 1.4.4). | Trusting a network only from the panel, with confirmation; warn in proxy mode. |

### 0.4 Checks and false alarms
| # | Failure | Zatvor rule / test |
|---|---|---|
| K23 | Background DNS TXT queries flagged by Kaspersky as `Trojan.Win32.DNSTun` (K 1.4.4). | No periodic TXT/odd DNS; ordinary A query to whoami.akamai.net at most every 10 min or on network change; TXT only in manual diagnostics. |
| K24 | Polling every 2 s while unhealthy slowed Windows PCs (K 1.5.2). | Back-off: fast checks for the first minute, then ≥10 s; network events still trigger immediately. CPU budget test. |
| K25 | DNS check manual vs background showed different results; stale observation shown as fresh after network change (K 1.5.1). | One source of truth per check, timestamped with network generation. |
| K26 | "DNS leak" shown when the real issue was the exit IP itself in proxy mode (K 1.4.1). | Report the root cause card only; DNS of the VPN itself doesn't pause in split routing. |
| K27 | Split routing: IP-check services disagreed; AI services silently went direct (K 1.4.0, 1.4.1). | Route check via `/cdn-cgi/trace` of each protected service; that is the decisive check in split routing. |
| K28 | Proxy-only apps (PAC, `~/.claude/settings.json`, `HTTPS_PROXY`) were checked via the wrong path (K 1.4.1). | Probe through the same proxy/PAC decision the app uses. |
| K29 | Time-zone warning stuck until restart (K 1.4.1). | Re-read TZ on every evaluation/OS event. |

### 0.5 Persistence
| # | Failure | Zatvor rule / test |
|---|---|---|
| K30 | Settings file saved empty → silent loss after restart (K 1.4.0). | Atomic write (temp + fsync + rename), never write empty over non-empty, schema version + migrations. Test: crash injection between steps. |
| K31 | Several settings reset after reboot (K 1.4.7). | Every setting round-trips through save/load in tests. |

### 0.6 Update and lifecycle
| # | Failure | Zatvor rule / test |
|---|---|---|
| K32 | New version didn't start after menu update on macOS (K 1.2.1, 1.3.1). | New version waits for old one to exit (≤2 min), launched detached via the OS (not as child of a terminal/IDE). |
| K33 | Supervisor not running after update (K 1.4.2). | New version verifies and starts the supervisor itself. |
| K34 | Old version deleted the discovery file the panel used; "not running" (K 1.4.2). | Discovery/lock files owned per-instance; recreated if missing. |
| K35 | Update killed when started from a terminal (K 1.3.1). | Updater detaches (new session/job). |
| K36 | Update with VPN off closed Kordon (K 1.5.2: 20 s wait too short). | Wait up to 2 min; rollback if new version fails to start. |
| K37 | Missing/revoked release gave 404 (K 1.4.6). | Fall back to the latest available signed release. |
| K38 | Interrupted update left a second app copy (K 1.4.6). | Staging dir cleaned on next start. |
| K39 | Can't replace app (permissions, App Translocation) discovered after download (K 1.4.3). | Pre-flight writability check. |
| K40 | Install scripts verified SHA-256 only from the same release. | Scripts verify Ed25519 signature of SHA256SUMS too. |

### 0.7 Platform UI
| # | Failure | Zatvor rule / test |
|---|---|---|
| K41 | Windows tray window opened blank (K 1.3.2); popup lost after sleep (K 1.4.4). | Health-check the pre-created window; recreate on failure; fallback native menu. |
| K42 | Tray menu re-rendered every second during countdown (K 1.5.0). | Re-render only on state-key change. |
| K43 | Second launch only said "already running" (K 1.5.4). | Second launch focuses the window. |
| K44 | Icon hidden under "^"; no Start menu entry when run from Downloads (K 1.5.4). | First-run hint to pin icon; Start menu shortcut. |
| K45 | macOS zombie processes from notifications/link opening hit the process limit (K 1.5.0). | Native notification API; reap all children; test count of zombies after N notifications. |
| K46 | Panel behind other windows on macOS (K 1.4.0). | Activate app before showing panel. |
| K47 | Icon under the notch unreachable (K 1.4.4). | Reopening the app from Finder/Spotlight shows the panel. |
| K48 | Hidden PowerShell for notifications triggered antivirus (K 1.4.1). | WinRT notifications, no helper processes. |
| K49 | Russian text garbled in PowerShell (K 1.4.5); `status > file` empty (K 1.4.2). | Console output via proper code page/UTF-8 handling, write to stdout handle not the console window. |
| K50 | Extension couldn't reach the app when port was taken; WSL2 mirrored mode reserves 4096-port blocks (K 1.4.2). | Fallback ports far apart; discovery via file, not fixed port. |
| K51 | Power Nap on Mac wakes with VPN down → noisy notifications (K 1.4.6). | Keep apps paused but suppress notifications during dark wake. |


Research date: 2026-11-19. Clients studied: **v2rayN** (2dust/v2rayN), **Throne** (throneproj/Throne, formerly Nekoray/NekoBox), **Hiddify** (hiddify/hiddify-app), **Clash Verge Rev** (clash-verge-rev/clash-verge-rev).

## Method and caveats

- For each repo, the 200 most-commented closed issues were pulled from the GitHub search API (`is:issue is:closed`, sorted by comments). From those, roughly 50-80 per repo that are real bugs were kept. Feature requests, how-to threads, "unified reply" pinned posts, spam and bot issues were dropped.
- This session could not call the GitHub REST issue or comment endpoints for these repos, because they returned 403. Issue bodies came from the search results. For about 110 of the most relevant issues, the full thread (comments and maintainer replies) was read from the public issue web page and summarized. All other issues are classified from their title and body only.
- **Root causes are frequently unconfirmed** in these trackers. Many issues are closed as stale or with a maintainer saying "try the latest build", without naming a commit. Each entry says "confirmed" only when a maintainer or a linked fix states the cause. "Suspected" means a user analysis. "Unknown" means nobody found it.
- Many failures come from the core (Xray, sing-box or mihomo), not the GUI. They are listed anyway, because users blame the GUI and Zatvor has to contain them: pin versions, validate configs, show clear errors.
- Abbreviations: **v2N** = v2rayN, **THR** = Throne, **HID** = Hiddify, **CVR** = Clash Verge Rev.

---

## 1. Architecture of each client

### v2rayN (2dust/v2rayN)
- **Language and UI:** C# on .NET (currently .NET 8+). The UI was WPF on Windows and has moved to **Avalonia** for cross-platform builds. Business logic lives in `ServiceLib` and uses ReactiveUI.
- **Core:** Xray-core, sing-box, mihomo and others run as **separate child processes**. The GUI writes `config.json` and starts the binary (`CoreManager.RunProcess`). On Windows, child processes are added to a **Job Object** so they die with the GUI. TUN or "pre-socks" mode can run **two cores**: a sing-box front that owns TUN, chained over SOCKS to Xray. Recent versions can also use Xray's own TUN.
- **TUN:** sing-box TUN (wintun) or Xray TUN. On Windows, the app removes a stale TUN adapter before it starts (`WindowsUtils.RemoveTunDevice`). On Linux and macOS, the core is launched through **sudo** with a password that the GUI collects (`CoreAdminManager.RunProcessAsLinuxSudo`). On Windows, the GUI has to run as administrator.
- **System proxy:** WinINet or registry on Windows. On macOS and Linux, shell scripts in `binConfigs` call `networksetup`, `gsettings` or `kwriteconfig`. The app has also shipped a PAC mode with a local PAC server.
- **Updater:** a separate `AmazTool` helper exe kills the GUI, unzips the new release and relaunches. Core and geo files are downloaded in-app. Releases are GPG-signed.
- **Storage:** a SQLite database plus `guiNConfig.json` beside the exe (portable layout).

### Throne (throneproj/Throne, ex-Nekoray)
- **Language and UI:** C++ with **Qt 6 Widgets**. It uses SQLite (sqlitecpp) for profiles.
- **Core:** a separate Go process, **ThroneCore**, that embeds sing-box (with Xray for some outbounds). The GUI controls it over **gRPC/protobuf** on localhost. The core checks its parent process ("unexpected parent", THR#1474). The GUI auto-restarts the core when it exits and gives up after too many exits. It can also run headless as a service, controlled by `thronectl`.
- **TUN:** sing-box `tun` inbound (sing-tun: system, gvisor or mixed stack; wintun on Windows; nftables auto-redirect on Linux). Privilege model: on Windows the whole app runs as admin. On Linux and macOS the core binary is made **setuid-root or given `cap_net_admin`** through a Terminal prompt. The README says to move the app to `/Applications` first, because AppTranslocation breaks this.
- **System proxy:** a native implementation per OS (registry, `networksetup`, gsettings and kwriteconfig).
- **Updater:** an in-app release check plus a separate updater step that unpacks the zip. Package managers (AUR, APT/RPM repos, Homebrew cask) are also used.

### Hiddify (hiddify/hiddify-app)
- **Language and UI:** **Flutter/Dart** (Riverpod). Desktop uses `window_manager`, `tray_manager`, `launch_at_startup` and the `upgrader` package. Sentry provides crash reporting.
- **Core:** **hiddify-core**, a Go wrapper around a sing-box fork that also carries Xray features. It is built as a shared library (`libcore.dll/.so/.dylib`) and loaded through **Dart FFI**. On desktop, a **"background core" / HiddifyCli** process is started and controlled over **gRPC**. "failed to start background core" (HID#1936, #2083, #2276, #1956) is the most common desktop error.
- **TUN:** sing-box TUN inside the core. It needs admin or root. On Linux it also needs CAP_NET_ADMIN, so it fails unless run with sudo (HID#1936, #2002). On Windows, "VPN Service" mode installs a service. Modes are system proxy, TUN ("VPN") and VPN Service.
- **Updater:** in-app version check (`upgrader`) that links to store or release downloads. Packaging covers MSIX/EXE, AppImage, deb/rpm and DMG. Mobile builds go through the stores.

### Clash Verge Rev (clash-verge-rev/clash-verge-rev)
- **Language and UI:** **Tauri 2**, with a Rust backend and a React/TypeScript frontend in a WebView (WebView2 on Windows, WKWebView on macOS, WebKitGTK on Linux).
- **Core:** **mihomo** (Clash.Meta) runs as a **separate process**. It is either a Tauri "sidecar" child (user mode) or started by the privileged **clash-verge-service** (a Windows service, launchd daemon or systemd unit). The GUI talks to the service over local IPC (a named pipe or unix socket, crate `clash_verge_service_ipc`) and to mihomo over its REST API (`tauri-plugin-mihomo`, a named pipe or unix socket on newer versions, a localhost port on older ones).
- **TUN:** mihomo's own TUN, which requires service mode or admin. If the service is unavailable, the app **falls back to sidecar mode**, and TUN does not work there (CVR#8035).
- **System proxy:** the `sysproxy-rs` crate. It also has a PAC mode served from a local port (33331) and a "proxy guard" that re-applies the proxy setting periodically.
- **Updater:** `tauri-plugin-updater`, which uses signed update manifests on GitHub releases. Autostart uses `tauri-plugin-autostart` (Startup-folder shortcut, registry Run key or Task Scheduler in admin mode). Global hotkeys use `tauri-plugin-global-shortcut`.

### Lessons from the architectures
- All four run the core as a **separate process**, or in Hiddify's case as an FFI library plus a separate background process. Every client has bugs where the GUI and the core disagree about state: blank node lists, "core communication error", orphaned core processes. The GUI-core link needs a health protocol (Section 2.4).
- The clients whose privileged part is a **service or helper** (CVR, HID VPN Service) have bugs in service install, upgrade compatibility and IPC. The clients that **run the whole GUI as admin or root** (v2N, THR, HID) have bugs with autostart, UAC and setuid on macOS. Zatvor should pick one model and design its upgrade path from day one.

---

## 2. Pitfalls by category

Each entry gives the failure, its cause, the evidence, and a **Zatvor rule or test**.

### 2.1 System proxy restore and leftover proxy after a crash, shutdown or uninstall

1. **System proxy left on after a crash, force-quit or OS shutdown leaves the user with no internet on the next boot.** Cause: the proxy is written to HKCU `ProxyEnable` (Windows) or to `networksetup` (macOS) and only reverted on a clean exit. Shutdown, a kill or a crash skips that step. Maintainers once said "can't be fixed" (CVR#776). Throne fixed it by adding "proper shutdown handling on all 3 platforms" (THR#731). CVR changed shutdown handling in PR #5023 (CVR#4769). Evidence: [CVR#776](https://github.com/clash-verge-rev/clash-verge-rev/issues/776), [CVR#4769](https://github.com/clash-verge-rev/clash-verge-rev/issues/4769), [HID#2306](https://github.com/hiddify/hiddify-app/issues/2306), [HID#1558](https://github.com/hiddify/hiddify-app/issues/1558), [HID#1492](https://github.com/hiddify/hiddify-app/issues/1492), [THR#731](https://github.com/throneproj/Throne/issues/731).
   **Zatvor rule:** store a "proxy-owned" journal before changing system proxy settings. Handle WM_QUERYENDSESSION/WM_ENDSESSION, SIGTERM and NSWorkspaceWillPowerOff. **At every start, before anything else**, if the journal shows we own a stale setting and no core is running, restore the snapshot. Also run that restore as a small logon task or helper even when the GUI is not set to autostart. *Test:* kill -9 the GUI and the core, reboot, and assert the proxy settings equal the pre-connect snapshot.

2. **Proxy settings never cleared on macOS, so the Mac is offline after quitting.** Cause: the proxy shell script lacked the execute bit, a `chmod` call depended on PATH, or the user was not admin. Evidence: [v2N#6970](https://github.com/2dust/v2rayN/issues/6970), [v2N#6462](https://github.com/2dust/v2rayN/issues/6462), [v2N#6746](https://github.com/2dust/v2rayN/issues/6746).
   **Zatvor rule:** do not use shell scripts. Call SystemConfiguration (or `networksetup` by absolute path) from native code and **check the exit code**. Read the settings back after each write and show an error if they differ. *Test:* a CI job on macOS verifies that set, read back, clear and read back all match.

3. **macOS: "admin privileges required to modify system proxy" for non-admin users, and `networksetup` exit status 14.** Evidence: [CVR#6519](https://github.com/clash-verge-rev/clash-verge-rev/issues/6519), [HID#869](https://github.com/hiddify/hiddify-app/issues/869), [HID#1061](https://github.com/hiddify/hiddify-app/issues/1061), [v2N#7884](https://github.com/2dust/v2rayN/issues/7884).
   **Zatvor rule:** decide at design time how the macOS proxy write gets authorization (a privileged helper via SMAppService, or an AuthorizationRef prompt). Test with a **standard (non-admin) macOS account**.

4. **Uninstalling or upgrading leaves the proxy (or TUN DNS) pointing at a dead port.** Evidence: [CVR#1095](https://github.com/clash-verge-rev/clash-verge-rev/issues/1095), [CVR#709](https://github.com/clash-verge-rev/clash-verge-rev/issues/709), [CVR#4921](https://github.com/clash-verge-rev/clash-verge-rev/issues/4921).
   **Zatvor rule:** the uninstaller and the in-app updater both call the same "restore network state" routine: proxy, DNS and routes. *Test:* uninstall while connected and assert the network works.

5. **The system proxy toggle switches itself off a few seconds after being enabled, or the UI shows on while the OS says off.** Cause: unknown; a regression in a proxy guard or state sync is likely. Evidence: [CVR#6832](https://github.com/clash-verge-rev/clash-verge-rev/issues/6832), [CVR#4136](https://github.com/clash-verge-rev/clash-verge-rev/issues/4136), [CVR#766](https://github.com/clash-verge-rev/clash-verge-rev/issues/766), [THR#552](https://github.com/throneproj/Throne/issues/552), [THR#1532](https://github.com/throneproj/Throne/issues/1532).
   **Zatvor rule:** derive the UI state **from reading the OS setting back**, not from an in-memory flag. Any "guard" that re-applies settings must log each change and must never fight the user's own toggle.

6. **Stray characters in the bypass list (semicolons, `http://` prefixes) make Windows reject the whole proxy setting (`Error setting options: 87`).** Evidence: [v2N#2716](https://github.com/2dust/v2rayN/issues/2716).
   **Zatvor rule:** validate and normalize the bypass list before writing it, show the error inline, and unit-test the WinINet bypass-string builder.

7. **Linux desktop proxy not honored (KDE or Fedora Firefox ignores it; Qt5 `kwriteconfig5` used on a Qt6 desktop).** Evidence: [v2N#6073](https://github.com/2dust/v2rayN/issues/6073), [THR#864](https://github.com/throneproj/Throne/issues/864).
   **Zatvor rule:** on Linux, support GNOME (gsettings) and KDE (kwriteconfig6, with kwriteconfig5 as fallback) explicitly and detect which is present. Show "unsupported desktop, use TUN or export env vars" elsewhere.

8. **The system proxy breaks DoH in Firefox: Firefox falls back to plaintext DNS when it sees a system proxy.** Evidence: [v2N#8255](https://github.com/2dust/v2rayN/issues/8255).
   **Zatvor rule:** document it, and prefer SOCKS with remote DNS in the PAC or proxy settings we write.

9. **Sandboxed UWP and Store apps (Microsoft Store, WhatsApp UWP) cannot reach a loopback proxy.** Cause: AppContainer loopback isolation. Evidence: [v2N#1083](https://github.com/2dust/v2rayN/issues/1083), [v2N#3414](https://github.com/2dust/v2rayN/issues/3414), [CVR#2415](https://github.com/clash-verge-rev/clash-verge-rev/issues/2415).
   **Zatvor rule:** offer a "UWP loopback exemption" tool (`CheckNetIsolation LoopbackExempt` or the Firewall API) that **handles the error 0x57 path** reported in CVR#2415. Otherwise point users to TUN mode.

10. **PAC mode silently sends traffic direct because the local PAC server did not start (port in use or not elevated).** Evidence: [CVR#4575](https://github.com/clash-verge-rev/clash-verge-rev/issues/4575), [v2N#307](https://github.com/2dust/v2rayN/issues/307).
    **Zatvor rule:** before pointing the OS at a PAC URL, fetch it from our own process. Fail loudly if it does not return HTTP 200.

### 2.2 TUN, routes and DNS leaks

11. **DNS leak in TUN mode: Windows "smart multi-homed name resolution" sends queries out of every interface when strict route is off.** Evidence: [CVR#3133](https://github.com/clash-verge-rev/clash-verge-rev/issues/3133), [THR#1442](https://github.com/throneproj/Throne/issues/1442).
    **Zatvor rule:** turn on strict route (WFP block of DNS outside the tunnel) by default on Windows. Include a **leak test** in CI: query a canary domain and capture on the physical NIC.

12. **DNS leak from a bad generated default config (local DNS detour set to `direct` in global mode).** Cause: confirmed by the maintainer. Evidence: [v2N#5372](https://github.com/2dust/v2rayN/issues/5372).
    **Zatvor rule:** golden-file tests of the generated core config for each mode (global, rule, direct) × (proxy, TUN), plus an assertion that no DNS server is reachable through `direct` in global mode.

13. **IPv6 (AAAA) lookups or traffic not hijacked, so they leak or fail. Some sites break unless IPv6 is disabled.** Evidence: [CVR#2329](https://github.com/clash-verge-rev/clash-verge-rev/issues/2329), [CVR#844](https://github.com/clash-verge-rev/clash-verge-rev/issues/844), [CVR#1762](https://github.com/clash-verge-rev/clash-verge-rev/issues/1762), [v2N#9878](https://github.com/2dust/v2rayN/issues/9878), [THR#1042](https://github.com/throneproj/Throne/issues/1042).
    **Zatvor rule:** IPv6 handling is an explicit setting. With it on, the TUN carries ::/0 and hijacks AAAA. With it off, block IPv6 in the tunnel (do not leak it) and return empty AAAA answers. Test both modes on a dual-stack network.

14. **A routing rule-set changed upstream and leaked the real IP (geosite:cn source switched to ChinaMax).** Cause: confirmed. Evidence: [THR#748](https://github.com/throneproj/Throne/issues/748).
    **Zatvor rule:** pin geo and rule-set sources to known upstreams with a checksum and a changelog diff. A leak test runs on every rule-set update.

15. **WebRTC or local-proxy deanonymization: any local app (including malware) can use the unauthenticated SOCKS inbound to learn the exit IP, which bypasses per-app split tunneling.** Evidence: [HID#2120](https://github.com/hiddify/hiddify-app/issues/2120), [THR#1343](https://github.com/throneproj/Throne/issues/1343), [THR#1267](https://github.com/throneproj/Throne/issues/1267).
    **Zatvor rule:** in TUN mode, close or authenticate the mixed inbound by default. In proxy mode, bind to 127.0.0.1 only and offer an auth option. Never listen on 0.0.0.0 unless the user enables LAN sharing.

16. **"Cannot create a file when that file already exists" / "file exists": a stale wintun adapter or a leftover nftables table blocks TUN start.** Evidence: [v2N#3853](https://github.com/2dust/v2rayN/issues/3853), [THR#1701](https://github.com/throneproj/Throne/issues/1701), [THR#1704](https://github.com/throneproj/Throne/issues/1704).
    **Zatvor rule:** give the TUN interface a fixed name and GUID. On start, detect and **delete our own stale adapter, nftables table and routes**. *Test:* kill -9 the core while TUN is up, then start again.

17. **TUN fails on the first start after reboot (interface timeout or "device not ready") and works on retry.** Cause: confirmed for Throne, which did not handle OS termination, so the adapter was left in a bad state. Evidence: [THR#331](https://github.com/throneproj/Throne/issues/331), [THR#1296](https://github.com/throneproj/Throne/issues/1296), [CVR#5049](https://github.com/clash-verge-rev/clash-verge-rev/issues/5049), [CVR#7177](https://github.com/clash-verge-rev/clash-verge-rev/issues/7177).
    **Zatvor rule:** retry TUN creation with backoff, up to about 30 s at boot. A startup failure must **never be saved as the user's setting**: CVR wrote `enable_tun_mode=false` to disk after one failed probe (CVR#7177).

18. **WFP errors (`FwpmFilterAdd0: invalid argument`) when switching configs with strict route on.** Evidence: [THR#1581](https://github.com/throneproj/Throne/issues/1581).
    **Zatvor rule:** tear down WFP filters and wait for the release to complete before the next config starts. Use a dynamic WFP session so filters die with the process.

19. **Routing loops in TUN: the core's own traffic, or its DNS to the TUN DNS address, is captured again (high CPU, log spam, frozen GUI).** Evidence: [THR#1513](https://github.com/throneproj/Throne/issues/1513) (fixed with sniffing and hijack-dns, commit 547e657), [THR#1365](https://github.com/throneproj/Throne/issues/1365), [THR#1442](https://github.com/throneproj/Throne/issues/1442), [THR#1737](https://github.com/throneproj/Throne/issues/1737), [CVR#6970](https://github.com/clash-verge-rev/clash-verge-rev/issues/6970) ("reject loopback connection").
    **Zatvor rule:** bind outbound sockets to the physical interface (auto-detect interface). Exclude the core process and the server IPs from the TUN routes. *Test:* a loop-detector metric (identical 5-tuples seen twice) that raises an alarm.

20. **TUN breaks on unusual uplinks: PPPoE dial-up, Hyper-V or VMware vSwitches, ZeroTier or Radmin adapters, hotspot sharing.** Evidence: [CVR#550](https://github.com/clash-verge-rev/clash-verge-rev/issues/550), [THR#1512](https://github.com/throneproj/Throne/issues/1512), [THR#1513](https://github.com/throneproj/Throne/issues/1513), [v2N#4539](https://github.com/2dust/v2rayN/issues/4539) (hotspot conflict), [HID#283](https://github.com/hiddify/hiddify-app/issues/283).
    **Zatvor rule:** pick the default interface from the **route table** (lowest-metric default route), not from adapter names. Re-pick it on change. Keep a test matrix with PPPoE and virtual adapters.

21. **TUN fake-IP DNS outlives the session: after TUN is off, apps keep cached 198.18.x.x answers and stay broken.** Evidence: [CVR#1095](https://github.com/clash-verge-rev/clash-verge-rev/issues/1095), [CVR#2325](https://github.com/clash-verge-rev/clash-verge-rev/issues/2325).
    **Zatvor rule:** use real-IP DNS by default, or **flush the OS DNS cache** (`DnsFlushResolverCache`, `dscacheutil -flushcache`, `resolvectl flush-caches`) on every TUN stop. Never force fake-ip over the user's choice silently, as CVR did (CVR#2325).

22. **The fake-IP range collides with the LAN (172.16/12 or 198.18/15 in use), and TUN breaks on that network.** Evidence: [CVR#2935](https://github.com/clash-verge-rev/clash-verge-rev/issues/2935).
    **Zatvor rule:** check the fake-IP and TUN subnets against local interface subnets at start, and pick a free one automatically.

23. **Local DNS names (`openwrt.lan`, `.local`, internal corp domains) stop resolving with TUN or "bypass LAN" on.** Evidence: [HID#1431](https://github.com/hiddify/hiddify-app/issues/1431), [CVR#5624](https://github.com/clash-verge-rev/clash-verge-rev/issues/5624), [THR#246](https://github.com/throneproj/Throne/issues/246).
    **Zatvor rule:** send single-label names, `.local`, `.lan`, the search domains and DHCP-provided domains to the system resolver by default.

24. **TUN works but certain apps or games break: UDP voice (PUBG), WeChat file transfer, Docker in WSL or VMware, remote-desktop tools.** Evidence: [v2N#7373](https://github.com/2dust/v2rayN/issues/7373), [CVR#1762](https://github.com/clash-verge-rev/clash-verge-rev/issues/1762), [CVR#1976](https://github.com/clash-verge-rev/clash-verge-rev/issues/1976), [CVR#1917](https://github.com/clash-verge-rev/clash-verge-rev/issues/1917), [CVR#2587](https://github.com/clash-verge-rev/clash-verge-rev/issues/2587).
    **Zatvor rule:** let users exclude processes (process-name direct rules plus TUN exclude-process). Test UDP and full-cone NAT, and WSL2 in NAT mode, before each release.

25. **macOS: the TUN "system" stack mishandles some UDP; switching to gvisor helps.** Evidence: [THR#246](https://github.com/throneproj/Throne/issues/246), [THR#1640](https://github.com/throneproj/Throne/issues/1640), [THR#1738](https://github.com/throneproj/Throne/issues/1738).
    **Zatvor rule:** default to the gvisor or mixed stack on macOS. Make the stack a setting with a "try another stack" hint in the error UI.

26. **IP leak through TUN during a reconnect or core restart.** Cause: traffic falls back to the physical NIC in the gap.
    **Zatvor rule:** add an optional kill switch (WFP or pf rule held while the core restarts). Default to "fail closed" in TUN mode.

### 2.3 Sleep/wake and network changes

27. **The GUI exits silently after sleep or resume or an RDP session switch, while the core keeps running headless.** Evidence: [CVR#4690](https://github.com/clash-verge-rev/clash-verge-rev/issues/4690), [CVR#1784](https://github.com/clash-verge-rev/clash-verge-rev/issues/1784).
    **Zatvor rule:** a supervisor test that suspends and resumes (`rundll32 powrprof.dll,SetSuspendState` in a VM) and switches RDP sessions. The GUI must survive, and if the GUI dies the core must not be orphaned.

28. **Windows Fast Startup restores stale core processes, leaving duplicate mihomo processes after a "shutdown".** Evidence: [CVR#2383](https://github.com/clash-verge-rev/clash-verge-rev/issues/2383).
    **Zatvor rule:** at start, find existing core processes **by path and our PID file**, and kill only ours. Do not rely on "shutdown always kills processes".

29. **Network outage leads to the core retrying in a tight loop, and logs grow to hundreds of GB.** Evidence: [CVR#3347](https://github.com/clash-verge-rev/clash-verge-rev/issues/3347) (a 409 GB log after two days offline), [THR#694](https://github.com/throneproj/Throne/issues/694).
    **Zatvor rule:** rotate logs by size (for example 10 MB × 5) and rate-limit identical messages. Use backoff on reconnects and pause activity while the OS reports no network.

30. **Autostart runs before the network or the service is ready, so the first connect fails and the failure is saved.** Evidence: [CVR#5623](https://github.com/clash-verge-rev/clash-verge-rev/issues/5623), [CVR#7177](https://github.com/clash-verge-rev/clash-verge-rev/issues/7177), [CVR#5049](https://github.com/clash-verge-rev/clash-verge-rev/issues/5049), [v2N#5296](https://github.com/2dust/v2rayN/issues/5296).
    **Zatvor rule:** at boot, wait for network and service readiness with a timeout, then connect. Keep transient failures in memory only.

31. **Interface change (Wi-Fi to Ethernet, VPN adapter added) leaves the core bound to the old interface ("No default interface").** Evidence: [THR#1247](https://github.com/throneproj/Throne/issues/1247).
    **Zatvor rule:** subscribe to route-change notifications (`NotifyRouteChange2`, `SCNetworkReachability`, netlink) and re-bind or restart the core. *Test:* toggle adapters in CI VMs.

32. **macOS App Nap throttles the backgrounded app, and proxied throughput collapses to a few KB/s on high-RTT links.** Evidence: [HID#2343](https://github.com/hiddify/hiddify-app/issues/2343).
    **Zatvor rule:** while connected, hold an `NSProcessInfo beginActivity(options: .userInitiated)` assertion, or set `NSAppSleepDisabled`. Keep the data path in the core process, not the UI process.

### 2.4 Core process lifecycle and GUI-core communication

33. **Blank proxy or node page and "core communication error" after boot or upgrade: the GUI queried the core before it was ready, or the core API changed.** Evidence: [CVR#1504](https://github.com/clash-verge-rev/clash-verge-rev/issues/1504), [CVR#4579](https://github.com/clash-verge-rev/clash-verge-rev/issues/4579), [CVR#4850](https://github.com/clash-verge-rev/clash-verge-rev/issues/4850), [CVR#6444](https://github.com/clash-verge-rev/clash-verge-rev/issues/6444), [CVR#7199](https://github.com/clash-verge-rev/clash-verge-rev/issues/7199), [CVR#6330](https://github.com/clash-verge-rev/clash-verge-rev/issues/6330), [CVR#3041](https://github.com/clash-verge-rev/clash-verge-rev/issues/3041).
    **Zatvor rule:** the GUI waits for a **readiness handshake** (health RPC with a version) before it renders state, then retries with backoff. Pin a compatible core version range and check it at start.

34. **The core fails after a core update because the core dropped a config field (Xray `allowInsecure` removed in 26.x; sing-box legacy DNS and special outbounds removed in 1.12-1.14).** Evidence: [v2N#8736](https://github.com/2dust/v2rayN/issues/8736), [v2N#9435](https://github.com/2dust/v2rayN/issues/9435), [v2N#6428](https://github.com/2dust/v2rayN/issues/6428), [v2N#7698](https://github.com/2dust/v2rayN/issues/7698), [v2N#7682](https://github.com/2dust/v2rayN/issues/7682), [v2N#10084](https://github.com/2dust/v2rayN/issues/10084), [v2N#10070](https://github.com/2dust/v2rayN/issues/10070), [THR#1295](https://github.com/throneproj/Throne/issues/1295).
    **Zatvor rule:** **bundle and pin** the core version and never let users replace it from inside the app with an untested release. Generated configs are validated with `xray run -test` (or the equivalent) in CI against the pinned and next core versions. Migrate deprecated fields at subscription import.

35. **A generic "Failed to run core, see log" error hides the real cause (bad node field, port conflict, uTLS fingerprint typo).** Evidence: [v2N#4750](https://github.com/2dust/v2rayN/issues/4750), [v2N#4735](https://github.com/2dust/v2rayN/issues/4735), [v2N#2919](https://github.com/2dust/v2rayN/issues/2919), [v2N#9439](https://github.com/2dust/v2rayN/issues/9439).
    **Zatvor rule:** capture the core's stderr and map known error patterns to human messages ("node X has invalid fingerprint 'rando'"). Validate each node before it reaches the core.

36. **Batch latency or speed tests spawn many cores or ports and fail once the node count grows (500+ nodes).** Evidence: [v2N#6428](https://github.com/2dust/v2rayN/issues/6428), [v2N#4548](https://github.com/2dust/v2rayN/issues/4548), [v2N#4750](https://github.com/2dust/v2rayN/issues/4750), [v2N#10142](https://github.com/2dust/v2rayN/issues/10142), [THR#1161](https://github.com/throneproj/Throne/issues/1161).
    **Zatvor rule:** test all nodes in **one** test core with many outbounds (or a bounded worker pool). Check each port with a real bind before use, and isolate a bad node so it fails alone, not the whole batch.

37. **Mixing synchronous and asynchronous reads of the core's stdout crashes the GUI (.NET "Cannot mix synchronous and asynchronous operation on process stream").** Evidence: [v2N#4750](https://github.com/2dust/v2rayN/issues/4750).
    **Zatvor rule:** one async reader per pipe, always drained, so a full pipe can never block the core.

38. **Exiting the GUI crashes or hangs: shutdown touches UI objects from background threads.** Cause: confirmed. Evidence: [v2N#4776](https://github.com/2dust/v2rayN/issues/4776), [v2N#8273](https://github.com/2dust/v2rayN/issues/8273) (Avalonia "call from invalid thread"), [THR#852](https://github.com/throneproj/Throne/issues/852).
    **Zatvor rule:** marshal every UI update to the UI thread (enforce with a lint or debug assert). The exit sequence is ordered: stop timers, then the core, then restore the network, then the UI.

39. **The core dies and auto-restart loops, then gives up silently ("core exits too frequently").** Evidence: [THR#1474](https://github.com/throneproj/Throne/issues/1474) (a Scoop symlink broke the parent-PID path check), [CVR#5208](https://github.com/clash-verge-rev/clash-verge-rev/issues/5208).
    **Zatvor rule:** compare paths after resolving symlinks (`realpath` or `GetFinalPathNameByHandle`). Make the restart policy visible to the user, and on give-up return to a safe state with the proxy restored.

40. **Orphan or duplicate cores after the GUI dies, or the updater kills another instance's core.** Evidence: [v2N#6102](https://github.com/2dust/v2rayN/issues/6102) (the updater killed other instances by process name), [CVR#5208](https://github.com/clash-verge-rev/clash-verge-rev/issues/5208), [THR#1442](https://github.com/throneproj/Throne/issues/1442).
    **Zatvor rule:** use a Windows Job Object or Linux `PR_SET_PDEATHSIG` for the core, plus a single-instance lock per user. Identify our processes **by full path plus PID file, never by name**.

41. **The core crashes on CPUs without AVX (VMs with kvm64 CPU type, older machines): exit code -1073741819.** Evidence: [CVR#7136](https://github.com/clash-verge-rev/clash-verge-rev/issues/7136), [THR#845](https://github.com/throneproj/Throne/issues/845) (Qt needs SSE4.2 and POPCNT).
    **Zatvor rule:** document the minimum CPU, build a baseline (GOAMD64=v1) core, and detect the crash code to show "CPU unsupported".

42. **The GUI hangs while quitting because the network manager interrupts the quit (Qt).** Evidence: [THR#560](https://github.com/throneproj/Throne/issues/560), [THR#469](https://github.com/throneproj/Throne/issues/469).
    **Zatvor rule:** put a hard timeout on exit, with network restore happening before any UI teardown.

### 2.5 Subscriptions: parsing and updating

43. **"Decode failed, not Base64": subscriptions in plain text, URL-safe Base64, unpadded Base64, or with CRLF are rejected.** Evidence: [v2N#767](https://github.com/2dust/v2rayN/issues/767), [v2N#2243](https://github.com/2dust/v2rayN/issues/2243).
    **Zatvor rule:** a tolerant decoder that tries standard and URL-safe Base64 with and without padding, then plain lines, then YAML or JSON. Fuzz-test it with a corpus of real subscription formats.

44. **Unknown or new fields in sing-box, Xray or Clash subscriptions abort the whole import ("unknown field tunnel-per-resolver", "fake_packets").** Evidence: [HID#2161](https://github.com/hiddify/hiddify-app/issues/2161), [HID#1967](https://github.com/hiddify/hiddify-app/issues/1967), [HID#483](https://github.com/hiddify/hiddify-app/issues/483), [HID#2044](https://github.com/hiddify/hiddify-app/issues/2044).
    **Zatvor rule:** skip unknown fields and unsupported nodes **per node** with a warning. Never fail the whole subscription for one node.

45. **Re-exporting or sharing a link loses parameters (fp, pbk, type, path, host, security=none), or xhttp `extra` is rejected.** Evidence: [THR#1170](https://github.com/throneproj/Throne/issues/1170), [THR#978](https://github.com/throneproj/Throne/issues/978), [v2N#8516](https://github.com/2dust/v2rayN/issues/8516), [v2N#8730](https://github.com/2dust/v2rayN/issues/8730), [THR#1887](https://github.com/throneproj/Throne/issues/1887).
    **Zatvor rule:** a round-trip property test: parse(link) → export → parse gives the same node, for every protocol and transport.

46. **Updating a subscription resets the selected node to the first entry (often an "info" pseudo-node), or duplicates the node in use.** Evidence: [v2N#2553](https://github.com/2dust/v2rayN/issues/2553), [CVR#5466](https://github.com/clash-verge-rev/clash-verge-rev/issues/5466), [CVR#5191](https://github.com/clash-verge-rev/clash-verge-rev/issues/5191), [CVR#3945](https://github.com/clash-verge-rev/clash-verge-rev/issues/3945), [THR#1753](https://github.com/throneproj/Throne/issues/1753), [THR#1246](https://github.com/throneproj/Throne/issues/1246).
    **Zatvor rule:** give each node a stable identity (hash of server, port, uuid and transport). Keep the selection through updates by that key, and never auto-select a node that fails validation.

47. **Subscription update fails behind the proxy or with TLS errors ("error sending request").** Evidence: [CVR#4301](https://github.com/clash-verge-rev/clash-verge-rev/issues/4301), [CVR#3064](https://github.com/clash-verge-rev/clash-verge-rev/issues/3064), [CVR#5014](https://github.com/clash-verge-rev/clash-verge-rev/issues/5014), [CVR#4061](https://github.com/clash-verge-rev/clash-verge-rev/issues/4061), [v2N#3128](https://github.com/2dust/v2rayN/issues/3128).
    **Zatvor rule:** update first through the active proxy, then direct, with clear errors, configurable User-Agent and timeouts. Never replace a good cached subscription with an empty or failed result.

48. **Subscription-Userinfo `total=0` (unlimited) shown as "quota exceeded".** Evidence: [HID#1974](https://github.com/hiddify/hiddify-app/issues/1974).
    **Zatvor rule:** treat 0 or missing values as unknown or unlimited, and unit-test the header parser on edge cases.

49. **The profile database grows without bound across subscription updates.** Evidence: [THR#1888](https://github.com/throneproj/Throne/issues/1888).
    **Zatvor rule:** delete stats and orphan rows with the node, VACUUM periodically, and add a test that 1000 updates keep the database size flat.

50. **Very slow subscription parsing (1 s became 40 s) or slow group properties dialogs with large groups.** Evidence: [THR#1323](https://github.com/throneproj/Throne/issues/1323), [THR#1460](https://github.com/throneproj/Throne/issues/1460).
    **Zatvor rule:** a performance budget test that parses a 10k-node subscription in under 1 s.

51. **Privacy: device-binding headers (HWID) sent to subscription providers without consent.** Evidence: [CVR#7187](https://github.com/clash-verge-rev/clash-verge-rev/issues/7187), [THR#1521](https://github.com/throneproj/Throne/issues/1521).
    **Zatvor rule:** send no device identifiers by default. Any extra header is opt-in per subscription and visible in the UI.

### 2.6 Routing rules and geo files

52. **Geo or rule-set file corrupt or wrong format, so the core will not start ("invalid sing-box rule-set file", "unknown rule item type: 170").** Evidence: [v2N#7682](https://github.com/2dust/v2rayN/issues/7682), [HID#1445](https://github.com/hiddify/hiddify-app/issues/1445), [THR#305](https://github.com/throneproj/Throne/issues/305).
    **Zatvor rule:** download to a temp file, verify size and checksum and test-load it with the core, then rename atomically. Keep the last good file and roll back to it.

53. **Geo files missing on first run, or the app cannot download them because they are needed before the proxy works.** Evidence: [HID#240](https://github.com/hiddify/hiddify-app/issues/240), [HID#1051](https://github.com/hiddify/hiddify-app/issues/1051), [CVR#3154](https://github.com/clash-verge-rev/clash-verge-rev/issues/3154).
    **Zatvor rule:** ship geo files in the installer, and update them later through the proxy once it is up.

54. **Rules from the "basic" tab silently ignored in preset routing modes, or merge and prepend rules ignored.** Evidence: [THR#1083](https://github.com/throneproj/Throne/issues/1083), [CVR#1292](https://github.com/clash-verge-rev/clash-verge-rev/issues/1292), [CVR#740](https://github.com/clash-verge-rev/clash-verge-rev/issues/740).
    **Zatvor rule:** show the user the **effective** merged rule list, and test the order (user rules before preset rules).

55. **Regex escapes in user scripts or rules break config parsing ("invalid escape").** Evidence: [CVR#3751](https://github.com/clash-verge-rev/clash-verge-rev/issues/3751).
    **Zatvor rule:** generate configs through a serializer (never string concatenation), with tests that include backslashes, quotes and Unicode in every user-editable field.

56. **Remote rule-sets auto-update without consent, or block startup when the network is down.** Evidence: [THR#701](https://github.com/throneproj/Throne/issues/701), [v2N#5219](https://github.com/2dust/v2rayN/issues/5219) ("initialize rule-set take too much time").
    **Zatvor rule:** core startup never waits on the network. Rule-set downloads run asynchronously against a cache.

### 2.7 Port conflicts

57. **The local inbound port (10808, 7890 and so on) cannot be bound because Hyper-V, WSL2 or Windows Sandbox reserved the range ("access forbidden" bind error).** Evidence: [v2N#1735](https://github.com/2dust/v2rayN/issues/1735), [v2N#4750](https://github.com/2dust/v2rayN/issues/4750).
    **Zatvor rule:** before starting, try-bind each port and query `netsh int ipv4 show excludedportrange`. Pick a free port automatically and tell the user.

58. **A fixed API or controller port (9097, 19090) collides with another app or a second instance.** Evidence: [CVR#2105](https://github.com/clash-verge-rev/clash-verge-rev/issues/2105), [CVR#1105](https://github.com/clash-verge-rev/clash-verge-rev/issues/1105).
    **Zatvor rule:** use a named pipe or unix socket for GUI-core control, or a random loopback port plus a secret token.

59. **"Allow LAN" mode silently stops working (with load balancing).** Evidence: [v2N#6268](https://github.com/2dust/v2rayN/issues/6268), [CVR#777](https://github.com/clash-verge-rev/clash-verge-rev/issues/777), [CVR#1494](https://github.com/clash-verge-rev/clash-verge-rev/issues/1494) (firewall).
    **Zatvor rule:** LAN sharing adds a scoped Windows Firewall rule and shows the actual listen address.

### 2.8 UI and tray

60. **The tray icon is missing, invisible on light or dark themes, or flickers on every speed update.** Evidence: [CVR#2840](https://github.com/clash-verge-rev/clash-verge-rev/issues/2840), [HID#620](https://github.com/hiddify/hiddify-app/issues/620), [v2N#4873](https://github.com/2dust/v2rayN/issues/4873) (Scoop symlink and the icon cache), [CVR#4679](https://github.com/clash-verge-rev/clash-verge-rev/issues/4679), [THR#545](https://github.com/throneproj/Throne/issues/545).
    **Zatvor rule:** separate monochrome template icons for light and dark (template image on macOS). Change the icon only on state changes, never per tick.

61. **The window does not appear, shows blank or black or white, or the GUI crashes due to GPU or renderer issues (Flutter black screen, AppImage "No GL implementation", WebView2 missing, Avalonia on ARM64).** Evidence: [HID#1057](https://github.com/hiddify/hiddify-app/issues/1057), [HID#1168](https://github.com/hiddify/hiddify-app/issues/1168), [HID#1760](https://github.com/hiddify/hiddify-app/issues/1760), [HID#1530](https://github.com/hiddify/hiddify-app/issues/1530), [HID#1599](https://github.com/hiddify/hiddify-app/issues/1599), [v2N#7850](https://github.com/2dust/v2rayN/issues/7850), [CVR#3051](https://github.com/clash-verge-rev/clash-verge-rev/issues/3051), [CVR#5381](https://github.com/clash-verge-rev/clash-verge-rev/issues/5381), [CVR#818](https://github.com/clash-verge-rev/clash-verge-rev/issues/818).
    **Zatvor rule:** provide a software-rendering fallback flag, applied automatically after a failed GPU init. Check prerequisites (WebView2 or GL) at launch with a native message box.

62. **The UI framework blocks NVIDIA Optimus GPU switching for other apps (WPF hardware acceleration).** Evidence: [v2N#5255](https://github.com/2dust/v2rayN/issues/5255).
    **Zatvor rule:** do not request the dGPU, and check that NVIDIA Control Panel "Auto" switching works with Zatvor running.

63. **"Start hidden" does not work at autostart; two instances fight (duplicate autostart entries: Scheduled Task plus Run key).** Evidence: [v2N#3259](https://github.com/2dust/v2rayN/issues/3259), [CVR#2130](https://github.com/clash-verge-rev/clash-verge-rev/issues/2130), [CVR#3656](https://github.com/clash-verge-rev/clash-verge-rev/issues/3656), [HID#1095](https://github.com/hiddify/hiddify-app/issues/1095), [HID#976](https://github.com/hiddify/hiddify-app/issues/976).
    **Zatvor rule:** a single autostart mechanism with migration that removes the old entries. A single-instance lock where a second launch activates the first instance.

64. **Global hotkeys hijack system shortcuts (Cmd+Q stops working on macOS).** Evidence: [CVR#1814](https://github.com/clash-verge-rev/clash-verge-rev/issues/1814).
    **Zatvor rule:** global hotkeys are off by default, and registration is checked for conflicts.

65. **Massive memory or a GUI freeze from an unbounded log view (selecting text in the log panel takes 64 GB; a WPF TextBox FailFast).** Evidence: [v2N#7644](https://github.com/2dust/v2rayN/issues/7644), [v2N#5169](https://github.com/2dust/v2rayN/issues/5169), [THR#1737](https://github.com/throneproj/Throne/issues/1737).
    **Zatvor rule:** the log view is a ring buffer (for example 5k lines), virtualized, with appends batched at no more than 10 Hz.

66. **Emoji and flag rendering broken on Linux or Windows; fonts missing.** Evidence: [v2N#8339](https://github.com/2dust/v2rayN/issues/8339), [THR#773](https://github.com/throneproj/Throne/issues/773), [THR#234](https://github.com/throneproj/Throne/issues/234).
    **Zatvor rule:** bundle an emoji font or strip emoji to text tags, and detect the font stack.

67. **Accessibility: the sidebar cannot be reached with a screen reader.** Evidence: [HID#2097](https://github.com/hiddify/hiddify-app/issues/2097).
    **Zatvor rule:** an accessibility audit (Narrator and VoiceOver) in the release checklist.

68. **Settings and selection not persisted (window size, last node, mode).** Evidence: [THR#846](https://github.com/throneproj/Throne/issues/846), [THR#215](https://github.com/throneproj/Throne/issues/215), [CVR#7852](https://github.com/clash-verge-rev/clash-verge-rev/issues/7852), [CVR#3945](https://github.com/clash-verge-rev/clash-verge-rev/issues/3945).
    **Zatvor rule:** persist the UI state through atomic writes, with a test that the state is equal after a restart.

69. **Localization overflow (Russian button titles too long).** Evidence: [THR#686](https://github.com/throneproj/Throne/issues/686).
    **Zatvor rule:** a pseudo-localization (+40% length) screenshot test.

### 2.9 Updater

70. **The updater kills the wrong processes (by name), or leaves the app half-updated.** Evidence: [v2N#6102](https://github.com/2dust/v2rayN/issues/6102), [CVR#3127](https://github.com/clash-verge-rev/clash-verge-rev/issues/3127), [CVR#4481](https://github.com/clash-verge-rev/clash-verge-rev/issues/4481).
    **Zatvor rule:** the updater targets PIDs found by path. It stages the update to a sibling directory, swaps atomically, keeps a rollback copy, and starts the new version with a health check that rolls back on failure.

71. **An upgrade loses all profiles or nodes (a cleanup routine deleted the active profile; a config save interrupted mid-write).** Evidence: [CVR#7175](https://github.com/clash-verge-rev/clash-verge-rev/issues/7175), [v2N#1743](https://github.com/2dust/v2rayN/issues/1743), [v2N#2247](https://github.com/2dust/v2rayN/issues/2247).
    **Zatvor rule:** write config as temp file → fsync → rename. Keep N rotating backups. Migrations are versioned, back up first, and are tested on old fixture data. No "orphan cleanup" ever deletes a file referenced by the current config.

72. **An upgrade breaks the privileged service (an old service is incompatible with the new GUI; reinstall on every launch).** Evidence: [CVR#5369](https://github.com/clash-verge-rev/clash-verge-rev/issues/5369), [CVR#5916](https://github.com/clash-verge-rev/clash-verge-rev/issues/5916), [CVR#3154](https://github.com/clash-verge-rev/clash-verge-rev/issues/3154), [CVR#8035](https://github.com/clash-verge-rev/clash-verge-rev/issues/8035), [CVR#8043](https://github.com/clash-verge-rev/clash-verge-rev/issues/8043), [CVR#7074](https://github.com/clash-verge-rev/clash-verge-rev/issues/7074).
    **Zatvor rule:** the service and the GUI exchange protocol versions. The updater upgrades the service in the same transaction. Include a test from release N-2 to N.

73. **An upgraded framework drops OS support without notice (.NET runtime "Unknown Hard Error" on Win10; icu.dll missing on Win10 1809; Qt 6.8 freezes on old macOS; GLIBC too new).** Evidence: [v2N#9373](https://github.com/2dust/v2rayN/issues/9373), [v2N#6854](https://github.com/2dust/v2rayN/issues/6854), [HID#1944](https://github.com/hiddify/hiddify-app/issues/1944), [THR#169](https://github.com/throneproj/Throne/issues/169), [HID#78](https://github.com/hiddify/hiddify-app/issues/78), [v2N#9717](https://github.com/2dust/v2rayN/issues/9717).
    **Zatvor rule:** a published minimum OS matrix, CI smoke tests on the oldest supported OS, and an updater that refuses to install a build whose minimum OS exceeds the host.

74. **Supply-chain integrity: users asking to verify assets match the commit.** Evidence: [THR#1093](https://github.com/throneproj/Throne/issues/1093), [THR#799](https://github.com/throneproj/Throne/issues/799).
    **Zatvor rule:** reproducible builds, signed releases (Authenticode, notarization, minisign or GPG), checksums, and an updater that verifies signatures before applying.

### 2.10 Installer and uninstaller

75. **The uninstaller deleted the user's Start Menu tiles and autostart entries.** Evidence: [CVR#4172](https://github.com/clash-verge-rev/clash-verge-rev/issues/4172).
    **Zatvor rule:** the uninstaller removes only files listed in its install manifest, never whole shared folders. Test it on a VM with other apps' shortcuts present.

76. **The .deb install hangs, or dependencies conflict (libwebkit2gtk version, libayatana, CentOS rpm deps).** Evidence: [v2N#7882](https://github.com/2dust/v2rayN/issues/7882), [CVR#2094](https://github.com/clash-verge-rev/clash-verge-rev/issues/2094), [CVR#2069](https://github.com/clash-verge-rev/clash-verge-rev/issues/2069), [CVR#1948](https://github.com/clash-verge-rev/clash-verge-rev/issues/1948), [CVR#1192](https://github.com/clash-verge-rev/clash-verge-rev/issues/1192), [HID#974](https://github.com/hiddify/hiddify-app/issues/974), [HID#450](https://github.com/hiddify/hiddify-app/issues/450).
    **Zatvor rule:** a CI matrix that installs on the oldest and newest supported Ubuntu, Debian and Fedora, with an AppImage or Flatpak as a self-contained fallback.

77. **The app is installed into Program Files but writes config next to the exe, and the writes fail or data is lost.** Evidence: [v2N#1743](https://github.com/2dust/v2rayN/issues/1743) (suspected).
    **Zatvor rule:** separate the install directory from per-user data (`%APPDATA%`, `~/Library/Application Support`, `$XDG_CONFIG_HOME`). Portable mode is an explicit marker file.

78. **Privileged service binary path writable by non-admins, so the service rejects the core or, worse, privilege escalation is possible.** Evidence: [CVR#3428](https://github.com/clash-verge-rev/clash-verge-rev/issues/3428) (privilege-escalation vulnerability: root service with an unauthenticated control API), [CVR#8035](https://github.com/clash-verge-rev/clash-verge-rev/issues/8035), [THR#354](https://github.com/throneproj/Throne/issues/354) (SUID core).
    **Zatvor rule:** install the service and core binaries in an admin-only ACL location. Authenticate the IPC (peer UID/SID check plus a signed caller). The service accepts only a fixed allow-list of operations, never "run this binary" or "this config path". Prefer `cap_net_admin` over SUID on Linux. Get an external security review before 1.0.

79. **The service start type is changed by optimizer or security tools, and the app waits about 2 minutes, then falls back to a degraded mode.** Evidence: [CVR#8129](https://github.com/clash-verge-rev/clash-verge-rev/issues/8129).
    **Zatvor rule:** detect "service installed but disabled or manual" and show a one-click repair. Never block the UI on the service.

80. **Linux privilege for TUN: setcap breaks `$ORIGIN` library loading; the sudo path is missing (`/bin/sudo` not found); `su` is used to set the proxy.** Evidence: [HID#1936](https://github.com/hiddify/hiddify-app/issues/1936), [HID#2002](https://github.com/hiddify/hiddify-app/issues/2002), [HID#1381](https://github.com/hiddify/hiddify-app/issues/1381), [HID#415](https://github.com/hiddify/hiddify-app/issues/415), [v2N#6095](https://github.com/2dust/v2rayN/issues/6095), [v2N#6068](https://github.com/2dust/v2rayN/issues/6068), [HID#1779](https://github.com/hiddify/hiddify-app/issues/1779).
    **Zatvor rule:** on Linux, ship a statically linked core (no `$ORIGIN` dependency) and use a polkit-authorized systemd helper. Never hard-code `/bin/sudo`.

### 2.11 Antivirus false positives

81. **Windows Defender flags the installer, the MSIX or the main exe as a Trojan and deletes it.** Evidence: [v2N#1157](https://github.com/2dust/v2rayN/issues/1157), [HID#1709](https://github.com/hiddify/hiddify-app/issues/1709), [HID#520](https://github.com/hiddify/hiddify-app/issues/520).
    **Zatvor rule:** Authenticode-sign everything (an EV certificate or Azure Trusted Signing). Submit each release to the Microsoft WDSI false-positive portal before announcing it. Avoid UPX or packers and self-extracting droppers. Track VirusTotal scores in release CI.

82. **Security software or "startup boosters" disable the service, or block the core.** Evidence: [CVR#8129](https://github.com/clash-verge-rev/clash-verge-rev/issues/8129), [HID#1196](https://github.com/hiddify/hiddify-app/issues/1196) (suspected).
    **Zatvor rule:** the diagnostics page shows the service state, the core's file hash and recent crash events.

### 2.12 Windows-specific paths, encoding and OS behavior

83. **Symlinked install paths (Scoop `current`) break the tray icon cache and the core's parent-PID check.** Evidence: [v2N#4873](https://github.com/2dust/v2rayN/issues/4873), [THR#1474](https://github.com/throneproj/Throne/issues/1474).
    **Zatvor rule:** resolve the real path once at start and use it everywhere. Add a Scoop and winget install test.

84. **Non-ASCII user names or paths (Chinese folder names) and locale-dependent number parsing ("Input string was not in a correct format" at autostart).** Evidence: [v2N#3259](https://github.com/2dust/v2rayN/issues/3259) (Convert.ToInt32 in a locale context), [v2N#4750](https://github.com/2dust/v2rayN/issues/4750) (a path with Chinese characters).
    **Zatvor rule:** use invariant-culture parsing everywhere. Run CI with a user profile path containing CJK characters and spaces, and with the tr-TR and de-DE locales.

85. **Running as admin changes behavior: autostart is unsupported, named-pipe checks fail, the TUN pipe is not found.** Evidence: [CVR#3284](https://github.com/clash-verge-rev/clash-verge-rev/issues/3284), [CVR#3288](https://github.com/clash-verge-rev/clash-verge-rev/issues/3288), [CVR#6287](https://github.com/clash-verge-rev/clash-verge-rev/issues/6287).
    **Zatvor rule:** the GUI always runs unelevated, and only the service is privileged. Test the "user launched it with Run as administrator" case as well.

86. **Windows on ARM64-specific crashes.** Evidence: [v2N#7850](https://github.com/2dust/v2rayN/issues/7850).
    **Zatvor rule:** native ARM64 builds tested on real hardware or an ARM64 runner.

### 2.13 macOS-specific

87. **"App is damaged and can't be opened" (Gatekeeper or quarantine on unsigned builds); a dylib built against a newer SDK symbol (`_kIOMainPortDefault`).** Evidence: [HID#1962](https://github.com/hiddify/hiddify-app/issues/1962), [HID#1984](https://github.com/hiddify/hiddify-app/issues/1984), [HID#2020](https://github.com/hiddify/hiddify-app/issues/2020), [HID#2089](https://github.com/hiddify/hiddify-app/issues/2089), [v2N#6304](https://github.com/2dust/v2rayN/issues/6304).
    **Zatvor rule:** Developer ID signing plus notarization, a `LSMinimumSystemVersion` that matches the deployment target, and a launch test on the oldest supported macOS.

88. **setuid core ignored, or AppTranslocation makes the bundle read-only, so TUN privilege escalation fails.** Evidence: [THR#808](https://github.com/throneproj/Throne/issues/808), [THR#531](https://github.com/throneproj/Throne/issues/531), [CVR#3639](https://github.com/clash-verge-rev/clash-verge-rev/issues/3639), [CVR#859](https://github.com/clash-verge-rev/clash-verge-rev/issues/859).
    **Zatvor rule:** use a proper privileged helper (SMAppService daemon) or a Network Extension. Never rely on setuid on macOS.

89. **launchd service missing HOME, so the state path falls back to a non-existent `/var/lib` and the service crash-loops; a stale translocated path is saved in its state.** Cause: confirmed. Evidence: [CVR#7333](https://github.com/clash-verge-rev/clash-verge-rev/issues/7333).
    **Zatvor rule:** daemon paths are absolute and platform-specific (`/Library/Application Support/Zatvor`). Never store translocated or temporary paths.

90. **Root-owned socket or config files created by the helper become unwritable for the user, so service install "fails".** Evidence: [CVR#5916](https://github.com/clash-verge-rev/clash-verge-rev/issues/5916), [CVR#3154](https://github.com/clash-verge-rev/clash-verge-rev/issues/3154).
    **Zatvor rule:** the helper creates user-facing files with the user's ownership, or keeps its own files elsewhere. The socket's mode and group are set explicitly.

91. **TUN DNS changes (for example set to 223.5.5.5) not restored on quit.** Evidence: [CVR#709](https://github.com/clash-verge-rev/clash-verge-rev/issues/709), [CVR#4921](https://github.com/clash-verge-rev/clash-verge-rev/issues/4921).
    **Zatvor rule:** snapshot every network service's DNS before the change and restore it on stop, crash recovery and uninstall (the same journal as entry 1).

92. **An `osascript` password prompt at every login.** Evidence: [CVR#2187](https://github.com/clash-verge-rev/clash-verge-rev/issues/2187).
    **Zatvor rule:** no osascript-based elevation. Use a helper registered once.

93. **The main window is lost after closing it (Dock and tray re-open broken); a white overlay appears on first open.** Evidence: [CVR#3353](https://github.com/clash-verge-rev/clash-verge-rev/issues/3353), [CVR#3795](https://github.com/clash-verge-rev/clash-verge-rev/issues/3795), [v2N#8207](https://github.com/2dust/v2rayN/issues/8207), [THR#1659](https://github.com/throneproj/Throne/issues/1659).
    **Zatvor rule:** handle `applicationShouldHandleReopen`. Test close → Dock click → tray click on each macOS release.

### 2.14 Performance: CPU, memory and disk

94. **The GUI's own charts or traffic graph burn CPU while hidden or unfocused.** Evidence: [CVR#994](https://github.com/clash-verge-rev/clash-verge-rev/issues/994), [CVR#1924](https://github.com/clash-verge-rev/clash-verge-rev/issues/1924).
    **Zatvor rule:** stop all polling and animations when the window is hidden. Measure idle CPU below 0.5% in CI.

95. **Per-connection process-name lookup (scanning `/proc`) at high connection churn (torrents) pegs the CPU.** Evidence: [THR#1090](https://github.com/throneproj/Throne/issues/1090), [CVR#2367](https://github.com/clash-verge-rev/clash-verge-rev/issues/2367).
    **Zatvor rule:** enable process matching only when a rule needs it, and cache it. Load-test with 2k connections per second.

96. **Memory grows to several GB over days (GUI or core).** Evidence: [CVR#3641](https://github.com/clash-verge-rev/clash-verge-rev/issues/3641), [THR#1070](https://github.com/throneproj/Throne/issues/1070), [THR#1119](https://github.com/throneproj/Throne/issues/1119), [v2N#5186](https://github.com/2dust/v2rayN/issues/5186).
    **Zatvor rule:** a 72-hour soak test with RSS limits for the GUI and the core, plus a heap profile endpoint in debug builds.

97. **Very high CPU in TUN mode right after enabling it.** Evidence: [v2N#4889](https://github.com/2dust/v2rayN/issues/4889), [v2N#5308](https://github.com/2dust/v2rayN/issues/5308), [THR#1822](https://github.com/throneproj/Throne/issues/1822).
    **Zatvor rule:** the CPU budget gets alarms in the soak test. Look for routing loops first (entry 19).

98. **Heavy disk I/O in TUN mode (about 60 MB/s), from a leftover ETW trace or verbose logging.** Evidence: [THR#694](https://github.com/throneproj/Throne/issues/694).
    **Zatvor rule:** log at warning level by default and never leave ETW or debug tracing enabled in release builds.

---

## 3. Cross-cutting rules (the short list)

1. **Network-state journal:** proxy, DNS, routes and firewall are snapshotted before every change and restored on stop, crash, boot and uninstall. (Entries 1, 2, 4, 21, 91.)
2. **Config writes are atomic** (temp → fsync → rename), with backups. **Migrations are tested on old data.** (Entries 49, 71.)
3. **Pin and test the core versions,** and validate every generated config with the core's test mode in CI. (Entries 12, 34.)
4. **A per-node failure is isolated:** parsing, testing and import never fail the whole batch. (Entries 36, 43, 44.)
5. **Identify processes by path plus PID file, never by name.** Use a single-instance lock and a Job Object or pdeathsig. (Entries 28, 39, 40.)
6. **Read OS state back after writing it.** The UI shows the real state. (Entry 5.)
7. **Transient boot failures are never saved** as user settings. (Entries 17, 30.)
8. **The privileged component is minimal,** authenticated, versioned and ACL-protected, and gets a security review. (Entries 72, 78.)
9. **Default to no leaks:** strict route, IPv6 handled, loopback inbound only and authenticated in TUN mode, and leak tests in CI. (Entries 11-15.)
10. **Sign everything** (Authenticode, notarization), send releases for AV false-positive review, and keep a minimum-OS matrix in CI. (Entries 73, 81, 87.)
