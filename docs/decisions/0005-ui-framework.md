# 0005: UI framework for zatvor-ui

Status: accepted, 2026-11-21 (stage 0c, Task 9). The macOS rows are pending Artur's check on a real Mac
(`proto/ui/RESULTS.md`, checklist). Revisit if that check fails a hard criterion.

## Context

zatvor-ui is the tray icon, the tray popup and the panel. It runs as a separate process and is a thin client
of zatvord's local API (spec §4.1, §4.5). Spec §4.4 sets the criteria:

- a popup next to the tray that opens instantly from hidden;
- correct placement on Win10/11 taskbars and under the macOS notch;
- dark/light following the system;
- no console windows;
- binary size and RAM of the hidden window;
- crash isolation from zatvord;
- WebView2 child processes that can be identified, so the freezer can exclude them (K1).

Spec §12 sets the budgets: popup open from hidden ≤ 150 ms; hidden UI ≤ 120 MB (target).

Both candidates were built in `proto/ui`, showing the same page: the approved Main mockup with Onest bundled.
They were measured only on GitHub Actions (`.github/workflows/proto-ui.yml`), on windows-latest and macos-latest.

## Options

- **Tauri 2** (2.12.7, Rust shell over WebView2/WKWebView). Mature tray API, and Clash Verge Rev ships
  tray + popup with it on Windows and macOS. Costs: a second language (Rust) and a Cargo dependency tree in
  the build.
- **Wails v3** (v3.1.0, Go). Status checked in 0c: v3.0.0 was tagged as stable on 2026-10-26 and v3.1.0 on
  2026-11-01 (`go list -m -versions`), after 34 betas. It is no longer alpha, so the brief's rule "alpha
  cannot win" does not apply, but it has been stable for less than a month. It has a built-in tray-attached
  window (`AttachWindow`, `HideOnFocusLost`), and it is the same language as zatvord.
- **systray + webview** (Kordon's stack) was not prototyped. Both candidates built and passed on the first
  CI run, so the fallback was not needed. It would mean placing the popup by hand on each OS.

## Measurements

Full-method runs 44928656786 and 44929300703 (values as run 1 / run 2). Method and raw JSON:
`proto/ui/RESULTS.md`, `proto/ui/results/`.

| Criterion (budget) | Tauri 2 Win | Wails v3 Win | Tauri 2 mac | Wails v3 mac |
|---|---|---|---|---|
| Open from hidden, screen pixels, median ms (≤ 150) | 38.5 / 52.2 | 48.2 / 42.4 | not measured on runner | not measured on runner |
| Open from hidden, in-page rAF, median ms (≤ 150) | 5.4 / 5.1 | 45.1 / 57.7 | 11.2 / 7.5 | 10.9 / 11.9 |
| Worst warm open (rAF), ms | 6.2 / 5.6 | 93.5 / 94.0 | 16.0 / 29.8 | 14.1 / 24.7 |
| First open after start (rAF), ms | 31.8 / 39.8 | 16.6 / 25.2 | **1076 / 255** (earlier run 810) | 88.5 / **767** (earlier run 1289) |
| Hidden RAM, private working set / phys_footprint, MB (≤ 120) | 58.7 / 58.3 | 61.8 / 64.5 | 57.2 / 63.9 | 79.8 / 61.2 |
| Hidden RAM, private bytes, MB | 93.5 / 91.0 | 138.5 / 141.2 | n/a | n/a |
| Hidden RAM, sum of working sets (counts shared pages again for each process), MB | 292 / 290 | 294 / 299 | 193 / 176 | 246 / 202 |
| Binary (exe / universal .app), MB | 3.4 | 10.1 | 7.3 | 18.1 |
| No console window | yes | yes | n/a | n/a |
| Dark/light follows system | light verified on runner; dark: Artur | same | Artur | Artur |
| Placement at tray / notch | tray clicks not scriptable on runners: Artur | same | Artur | Artur |
| Crash isolation | separate process | separate process | separate process | separate process |
| Web engine processes identifiable (K1) | 6 msedgewebview2.exe, all descendants by PPID | same | WebKit XPC helpers via responsible PID (launchd children) | same |

## Decision

**Tauri 2.** Both frameworks meet the §12 budgets for warm opens and for hidden memory, measured as the
OS shows it to users. Tauri is ahead on every measured axis:

- Its show is asynchronous, and the page's next frame arrives in about 5 ms on Windows. Wails' `Show()`
  blocks for 25–83 ms, and its worst open was about 94 ms, close to the 150 ms budget.
- Its binary is a third of the size: 3.4 vs 10.1 MB on Windows, 7.3 vs 18.1 MB for the mac app.
- Its private bytes on Windows are lower: about 92 vs 140 MB.

It also has the longer track record in shipping tray apps, and that matches the spec's expectation. Wails'
single-language advantage does not outweigh this, because the UI is a separate process behind the local API
in either case. Wails v3 has also been stable for less than a month.

## Consequences

- Stage 1 adds a Rust toolchain (pinned, as in `proto/ui`), `tauri-cli` via npm and a Cargo.lock to CI.
  Cargo dependency licences go into the release licence check and NOTICE.
- **First open on macOS** can take up to about 1 s (cold WKWebView, Metal shader compile). zatvor-ui must
  pre-warm the popup at start, by showing it once off-screen or transparent, and stage 1 must measure the
  first open on Artur's Mac against the 150 ms budget.
- Hidden memory: on Windows, use WebView2's `MemoryUsageTargetLevel = Low` (and `IsVisible = false`) while
  the popup is hidden. The rAF probe shows that wry keeps the page "visible" while it is hidden. The hidden
  UI is about 60 MB today, half of the target.
- K1: the freezer's exclusions for zatvor-ui are its process tree by parent PID on Windows (msedgewebview2.exe
  children of zatvor-ui.exe, never by name alone, because other apps run WebView2 too). On macOS they are the
  WebKit/Metal XPC helpers whose responsible PID is zatvor-ui.
- Popup placement uses `TrayIcon::rect()` and the monitor work area, with our own code (`place()` in the
  prototype). It is not yet verified on left/top/right taskbars, Win10, or under the notch: stage 1 checks
  these with Artur.
- Fonts are bundled (Onest, OFL), with no network requests from the UI. The CSP allows only `self`.
- The macOS builds are ad-hoc signed only. Gatekeeper's "Open Anyway" flow is part of the UX until there is
  an Apple Developer ID (spec §11).
