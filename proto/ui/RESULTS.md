# Stage 0c, task 9: tray + popup, Tauri 2 vs Wails v3

Throwaway prototypes. Decision: `docs/decisions/0005-ui-framework.md`. No UI process was run on a
developer machine: everything below comes from GitHub Actions (`.github/workflows/proto-ui.yml`).

## What was built

Both prototypes show the same page, `shared/popup.html`. It is a static copy of the approved "Main" mockup:
iris, status, tunnel card, four app rows, bottom buttons. Colours are tokens, with a light set taken from the
TrayLight mockup. Onest is bundled as `shared/fonts/*.woff2` (SIL OFL 1.1, @fontsource-variable/onest 5.3.1),
and the page makes no network requests (CI checks this). The tray icon is the small iris, drawn by
`shared/icons/gen.py`: a colour PNG on Windows, a template image on macOS.

| | Tauri 2 (`tauri/`) | Wails v3 (`wails/`) |
|---|---|---|
| Versions | tauri 2.12.7, tauri-build 2.7.6, @tauri-apps/cli 2.12.6, Rust 1.97.0 | wails v3.1.0 (stable since v3.0.0 on 2026-10-26; v3.1.0 on 2026-11-01), Go 1.27.4 |
| Popup | pre-created hidden webview window, 360×560, no decorations, skip taskbar, always on top | same, through `WebviewWindowOptions` (`Hidden`, `Frameless`, `HiddenOnTaskbar`, `HideOnFocusLost`) |
| Tray placement | own `place()` from `TrayIcon::rect()`: above a bottom taskbar, below a top bar, beside a side taskbar, clamped to the work area. No positioner plugin was needed | `SystemTray.AttachWindow` + `PositionWindow` (built in) |
| Focus loss | hides on `Focused(false)`; a click within 250 ms of that hide does not reopen the popup | built-in `HideOnFocusLost`, with Wails' own Windows "justClosed" debounce |
| No console | `windows_subsystem = "windows"` | `-ldflags -H windowsgui` |
| macOS | `ActivationPolicy::Accessory`, ad-hoc signed universal `.app` (`signingIdentity: "-"`) | `ActivationPolicyAccessory`, a universal `.app` assembled by lipo with `LSUIElement`, ad-hoc signed |

The Go module is `proto/ui/go.mod`, separate from the root module. It holds `wails/`, `shared/` (embed) and
`pixelwatch/`; the root `go.mod` does not gain Wails.

## Method (`--self-test`, `selftest.sh`, `selftest-win.ps1`, `pixelwatch/`)

A runner cannot click the tray, so each prototype measures itself:

1. It starts hidden and waits 60 s. `hiddenRssMB` is the sum of the working set (Windows) or the resident
   size (macOS) of its own process and its descendants by parent PID: WebView2's `msedgewebview2.exe`
   processes are children and grandchildren. On macOS the WebKit XPC helpers are children of launchd, so they
   are attributed by *responsible PID* (`responsibility_get_pid_responsible_for_pid`). `childProcesses`
   lists the name and PID of each. These sums double-count shared pages, so they are an upper bound.
   For the same tree the self-test also records:
   - Windows: private bytes, and the **private working set** (the "Memory" column in Task Manager), read
     from perf counters 50 s after start while the popup is still hidden;
   - macOS: **phys_footprint** (the "Memory" column in Activity Monitor), via `proc_pid_rusage`.
2. Then 20 toggles from code, about 1 s apart: position at the tray, take t0, `show()`, and eval a probe.
   The probe's `requestAnimationFrame` callback reports back to the host (Tauri IPC / a Wails asset-server
   URL). `openMs` is the median time from t0 to that report. The first show (cold) is reported on its own.
3. On Windows only, `pixelwatch` is an **external** watcher, the same program for both frameworks. It reads
   one screen pixel inside the popup's rectangle and logs every change. `screenMs` is the time from t0 to
   the first read that shows the popup's colour. The poll interval is about 19 ms (each BitBlt from the
   screen waits for DWM), so the resolution is about 20 ms. That is enough against a 150 ms budget. On
   Windows the rAF probe found the page "visible" before show(), so WebView2 keeps rendering while hidden
   and the rAF time can come before the pixels. Treat `screenMs` as the honest number on Windows. On macOS
   the page was "hidden" at eval and "visible" at rAF, so rAF waits for the real show.
4. `binaryMB` is the size of the release exe; `bundleMB` is the size of the macOS `.app`.

## Results

Two full-method runs: 44928656786 and 44929300703. Earlier run 44927605217 used only the in-app method and
agrees with them. Raw output: `results/<framework>-<os>-run<id>.json`. Runners: Windows Server 2025
(1024×768, light theme, WebView2 157.0.4341.46); macOS 26.7.2 on virtual Apple M1/M2 Pro (light theme).

| Metric (budget, spec §12) | Tauri Win | Wails Win | Tauri mac | Wails mac |
|---|---|---|---|---|
| Popup open, screen pixels, median ms (≤ 150) | 38.5 / 52.2 | 48.2 / 42.4 | not measured: no screen-read on the mac runner | same |
| Popup open, rAF probe, median ms | 5.4 / 5.1 | 45.1 / 57.7 | 11.2 / 7.5 | 10.9 / 11.9 |
| rAF probe, max of 19 (excluding first) ms | 6.2 / 5.6 | 93.5 / 94.0 | 16.0 / 29.8 | 14.1 / 24.7 |
| First show after start, ms (screen / rAF) | 41.3, 35.7 / 31.8, 39.8 | 35.2, 53.9 / 16.6, 25.2 | rAF 1076 / 255 (run 1: 810) | rAF 88.5 / 767 (run 1: 1289) |
| show() call returns, ms | 0.0 (async) | 25–83 (blocks) | 0.0 | 0.1–2.6 |
| Hidden: private working set (Win, external, 50 s; not in metrics.json) / phys_footprint (mac, in-app, 60 s) MB (≤ 120) | **58.7 / 58.3** | **61.8 / 64.5** | **57.2 / 63.9** | **79.8 / 61.2** |
| Hidden: private bytes (commit) MB | 93.5 / 91.0 | 138.5 / 141.2 | n/a | n/a |
| Hidden: working-set / RSS sum MB (upper bound) | 292.0 / 290.0 | 294.2 / 298.7 | 193.3 / 176.3 | 246.3 / 202.1 |
| Processes (hidden) | exe + 6 msedgewebview2.exe (all by PPID) | exe + 6 msedgewebview2.exe | app + WebContent, Networking, 2× MTLCompilerService (by responsible PID) | app + WebContent, Networking, GPU, 2× MTLCompilerService, audio SandboxHelper |
| Binary MB | 3.4 | 10.1 | 7.1 exe / 7.3 .app (universal) | 17.9 exe / 18.1 .app (universal) |
| No console window | yes (GUI subsystem) | yes | n/a | n/a |
| Dark/light follows system | light verified on the runner (popup pixel #ffffff = light tokens); dark not measured on runner | same | not measured on runner (Artur) | same |
| Placement under tray / notch | not measured: no tray click on runners | same | Artur | Artur |

Reading the numbers:
- **Open time: no difference within noise.** Both pass the budget by a wide margin on Windows, and the pixel
  watcher's medians overlap within its 20 ms resolution. The Windows first open was slightly faster with
  Wails. The rAF medians (Tauri about 5 ms, Wails about 45–58 ms on Windows) are an artefact of the method:
  - Wails' `Show()` blocks inside the timed interval (InvokeSync onto the UI thread plus WebView2 `Show`),
    while Tauri's show() returns at once;
  - the two report back over different transports;
  - on Windows the probe fires before the pixels appear.

  Do not use them to compare the frameworks.
- On macOS the **first** show after start ranged from 0.09 to 1.3 s for both frameworks, erratically from
  run to run: a cold WKWebView, with Metal shaders compiled by MTLCompilerService. That breaks the 150 ms
  budget for the first open. Later opens take 5–30 ms.
- **Memory metric for the 120 MB budget:** the private working set (Windows, Task Manager "Memory") and
  phys_footprint (macOS, Activity Monitor "Memory"). These count each page once, and they are what users
  see. On that reading both pass, at 57–80 MB, and macOS is a tie.
  - The working-set sum (about 290 MB on Windows) counts shared WebView2 pages again for each process.
  - Under a commit (private bytes) reading, Tauri's about 91 MB fits and Wails' about 141 MB does not.
- The Windows private working set is read **externally** at 50 s by `selftest-win.ps1`, while the popup is
  still hidden, and is saved as `results/*-memory.json` in the CI artifact. It is **not** in metrics.json:
  the in-app readings there are taken at 60 s.
- What separates the two clearly: Tauri's binary is about a third of Wails' size on both OSes, and its
  private bytes on Windows are about 50 MB lower.

## Checklist for Artur's Mac

### First round (run 44929300703), 2026-11-21

Artur ran both builds on his MacBook (arm64, notch). Self-test numbers: Tauri openMs 23.8 (first 21.1),
footprint 20.8 MB hidden; Wails openMs 29 (first 22), footprint 26.3 MB. By hand:

- Tauri: "starts, but nothing is visible". Cause found in code: the popup was placed through
  `monitor_from_point`, which on macOS takes points while the tray rect is in physical pixels. On a Retina
  screen the lookup failed for any icon right of the screen's middle, so the popup was never moved next to
  the icon. Placement is now a pure function over physical monitor bounds (`popup_position`, unit-tested
  with Retina numbers). Whether the icon itself was hidden by the notch is not known; the new log answers it.
- Wails: icon and popup appeared, "buttons are not clickable". The popup was a static copy of the mockup,
  so clicks had no visible effect. Every control now gives feedback (mock only, nothing connects).
- Light/dark was not checked: the checklist sent him to System Settings. The popup's "..." menu now forces
  light or dark.

Changes for the second round: both apps open the popup once at start (visible even if the notch hides the
icon) and log every step to `~/Library/Logs/ZatvorProto/<tauri|wails>.log`. CI (macOS runner) now runs
`uicheck.sh`: `--ui-check` opens the popup through the click path, checks it is on screen and clicks every
control from the page; then a normal start must open the popup. Screenshots go to the metrics artifact.

### Second round

Download `proto-ui-tauri-macos-universal` and `proto-ui-wails-macos-universal` from proto-ui run
44969810051 (green, `uicheck.sh` passed for both on the macOS runner), unzip (the inner zip too), replace the apps in /Applications, and for each app:

1. **Start:** the popup opens by itself near the top right within about 2 s. Is an iris icon in the menu
   bar? If not, the notch is hiding it (note that).
2. **Clicks:** the switch turns protection off and on, "Пауза" becomes "Продолжить", "Проверить" and
   "Панель" show a short message. Everything is a mock.
3. **Theme:** "..." (top right) → Светлая / Тёмная / Как в системе. Colours change at once.
4. **Tray:** click the icon: the popup closes; click again: it opens under the icon. Click elsewhere: it
   closes. Right click on the icon: menu with "Выход".
5. **Log:** send `~/Library/Logs/ZatvorProto/tauri.log` and `wails.log` (Finder → Go → Go to Folder).

## Limits

- Tray clicks, the placement relative to the tray and the notch, and dark mode were not exercised on the
  runners. The Mac checklist covers them; Windows taskbar positions (left/top/right, Win10) are left for
  Artur's own taskbar, if he agrees.
- Runners differ from laptops: 4-core VMs, a software or virtual GPU, a 1024×768 screen, light theme.
- The measurement code (sysinfo, perf counters, ps) adds a little to binary size and memory.
- Wails v3 was built without `wails3` CLI packaging (plain `go build` + a hand-made `.app`), so it has no exe
  icon or version resource on Windows. This does not affect the measured numbers.
