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
| Hidden: private working set / phys_footprint MB (≤ 120) | **58.7 / 58.3** | **61.8 / 64.5** | **57.2 / 63.9** | **79.8 / 61.2** |
| Hidden: private bytes (commit) MB | 93.5 / 91.0 | 138.5 / 141.2 | n/a | n/a |
| Hidden: working-set / RSS sum MB (upper bound) | 292.0 / 290.0 | 294.2 / 298.7 | 193.3 / 176.3 | 246.3 / 202.1 |
| Processes (hidden) | exe + 6 msedgewebview2.exe (all by PPID) | exe + 6 msedgewebview2.exe | app + WebContent, Networking, 2× MTLCompilerService (by responsible PID) | app + WebContent, Networking, GPU, 2× MTLCompilerService, audio SandboxHelper |
| Binary MB | 3.4 | 10.1 | 7.1 exe / 7.3 .app (universal) | 17.9 exe / 18.1 .app (universal) |
| No console window | yes (GUI subsystem) | yes | n/a | n/a |
| Dark/light follows system | light verified on the runner (popup pixel #ffffff = light tokens); dark not measured on runner | same | not measured on runner (Artur) | same |
| Placement under tray / notch | not measured: no tray click on runners | same | Artur | Artur |

Reading the numbers:
- Both pass the open budget by a wide margin on Windows. The pixel watcher cannot tell the two apart within
  its 20 ms resolution. Inside the app, Tauri's show() is asynchronous and its frame arrives in about 5 ms.
  Wails' `Show()` blocks the caller for 25–83 ms (InvokeSync onto the UI thread plus WebView2 `Show`) and
  its tail is longer (max about 94 ms).
- On macOS the **first** show after start ranged from 0.09 to 1.3 s for both frameworks: a cold WKWebView,
  with Metal shaders compiled by MTLCompilerService. That breaks the 150 ms budget for the first open. Later
  opens take 5–30 ms.
- Hidden memory measured the way users see it (Task Manager / Activity Monitor) is 57–80 MB for both, under
  the 120 MB target. The raw working-set sum (about 290 MB on Windows) fails it, but it counts shared
  WebView2 pages once per process.
- Tauri's binary is about a third of Wails' size on both OSes.

## Checklist for Artur's Mac

Download `ZatvorProtoTauri-macos-universal.zip` (artifact `proto-ui-tauri-macos-universal`) and
`ZatvorProtoWails-macos-universal.zip` (artifact `proto-ui-wails-macos-universal`) from run 44929300703.
Unzip both, move them to /Applications, and for each app:

1. **Open from the menu bar:** start the app; an iris appears in the menu bar and there is no Dock icon.
   Click it: the popup opens. Click it again, or click elsewhere: the popup closes. Note whether the first
   open feels slow.
2. **Placement under the notch:** is the popup directly under the icon, inside the screen, and not hidden
   by the notch? Try once with the icon near the notch (move it with ⌘-drag).
3. **Dark/light:** switch System Settings → Appearance between Light and Dark with the popup open, then
   reopen it. The colours should follow without a restart.
4. **Gatekeeper prompt:** copy the exact text of the first-launch dialog, and say whether
   System Settings → Privacy & Security → "Open Anyway" was needed. The builds are ad-hoc signed and not
   notarized.
5. **metrics.json:** run `/Applications/ZatvorProtoTauri.app/Contents/MacOS/zatvor-proto-tauri --self-test`
   (and the same for `ZatvorProtoWails.app/Contents/MacOS/zatvor-proto-wails`). Wait about 90 s without
   touching anything, then send `/Applications/<App>.app/Contents/MacOS/metrics.json`. If that folder was
   not writable, the file is `~/zatvor-proto-<tauri|wails>-metrics.json`.

## Limits

- Tray clicks, the placement relative to the tray and the notch, and dark mode were not exercised on the
  runners. The Mac checklist covers them; Windows taskbar positions (left/top/right, Win10) are left for
  Artur's own taskbar, if he agrees.
- Runners differ from laptops: 4-core VMs, a software or virtual GPU, a 1024×768 screen, light theme.
- The measurement code (sysinfo, perf counters, ps) adds a little to binary size and memory.
- Wails v3 was built without `wails3` CLI packaging (plain `go build` + a hand-made `.app`), so it has no exe
  icon or version resource on Windows. This does not affect the measured numbers.
