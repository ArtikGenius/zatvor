// Stage 0c tray + popup prototype, Tauri 2. Throwaway: see proto/ui/RESULTS.md.
//
// Tray icon (small iris); left click toggles a 360x560 borderless popup that is
// created hidden at start and placed next to the tray icon; the popup hides on
// focus loss. On a normal start the popup also opens once by itself, so the
// app is visibly running even when the menu bar hides the icon (notch).
// Every step is logged to a file (see log_path) for a manual check.
// `--ui-check` (CI) opens the popup through the click path, clicks every
// control from the page and writes ui-check.json next to the exe.
// `--self-test` measures the budgets from spec §12 without a tray
// click (runners cannot click the tray) and writes metrics.json next to the exe.

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::Mutex;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use serde_json::{json, Value};
use tauri::image::Image;
use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{
    AppHandle, Manager, PhysicalPosition, Rect, WebviewUrl, WebviewWindow, WebviewWindowBuilder,
    WindowEvent,
};

const W: f64 = 360.0;
const H: f64 = 560.0;
const GAP: f64 = 8.0;
const TOGGLES: usize = 20;

struct State {
    // Painted reports from the page: (id, visibility at eval, at rAF), time of arrival.
    painted_tx: Mutex<Sender<(u32, String, String, Instant)>>,
    last_blur_hide: Mutex<Option<Instant>>,
    self_test: bool,
}

#[tauri::command]
fn ui_log(msg: String) {
    log(&format!("page: {msg}"));
    if let Some(tx) = UI_TX.get() {
        let _ = tx.lock().unwrap().send(msg);
    }
}

static UI_TX: std::sync::OnceLock<Mutex<Sender<String>>> = std::sync::OnceLock::new();
static LOG: std::sync::OnceLock<Mutex<Option<std::fs::File>>> = std::sync::OnceLock::new();

/// ~/Library/Logs/ZatvorProto/tauri.log (macOS), %LOCALAPPDATA%\ZatvorProto\tauri.log (Windows).
fn log_path() -> PathBuf {
    let base = if cfg!(target_os = "macos") {
        PathBuf::from(std::env::var_os("HOME").unwrap_or_default()).join("Library/Logs")
    } else {
        PathBuf::from(std::env::var_os("LOCALAPPDATA").or_else(|| std::env::var_os("TEMP")).unwrap_or_default())
    };
    base.join("ZatvorProto").join("tauri.log")
}

fn log(msg: &str) {
    use std::io::Write;
    let f = LOG.get_or_init(|| {
        let p = log_path();
        let _ = std::fs::create_dir_all(p.parent().unwrap_or(Path::new(".")));
        Mutex::new(std::fs::OpenOptions::new().create(true).append(true).open(p).ok())
    });
    let t = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_millis()).unwrap_or(0);
    if let Some(f) = f.lock().unwrap().as_mut() {
        let _ = writeln!(f, "{t} {msg}");
    }
}

#[tauri::command]
fn painted(state: tauri::State<'_, State>, id: u32, vis_eval: String, vis_raf: String) {
    let now = Instant::now();
    let _ = state.painted_tx.lock().unwrap().send((id, vis_eval, vis_raf, now));
}

fn main() {
    let self_test = std::env::args().any(|a| a == "--self-test");
    let ui_check = std::env::args().any(|a| a == "--ui-check");
    let (ui_tx, ui_rx) = channel::<String>();
    let _ = UI_TX.set(Mutex::new(ui_tx));
    log(&format!("start {} self_test={self_test} ui_check={ui_check}", tauri::VERSION));
    let (tx, rx) = channel();

    tauri::Builder::default()
        .manage(State {
            painted_tx: Mutex::new(tx),
            last_blur_hide: Mutex::new(None),
            self_test,
        })
        .invoke_handler(tauri::generate_handler![painted, ui_log])
        .setup(move |app| {
            #[cfg(target_os = "macos")]
            app.set_activation_policy(tauri::ActivationPolicy::Accessory);

            let popup = WebviewWindowBuilder::new(app, "popup", WebviewUrl::App("popup.html".into()))
                .title("Zatvor")
                .inner_size(W, H)
                .resizable(false)
                .decorations(false)
                .visible(false)
                .focused(false)
                .skip_taskbar(true)
                .always_on_top(true)
                .build()?;

            let handle = app.handle().clone();
            popup.on_window_event(move |ev| {
                if let WindowEvent::Focused(false) = ev {
                    let st = handle.state::<State>();
                    if st.self_test || ui_check {
                        return; // a runner may not give us focus; keep the popup up
                    }
                    if let Some(w) = handle.get_webview_window("popup") {
                        log("popup lost focus: hide");
                        let _ = w.hide();
                        *st.last_blur_hide.lock().unwrap() = Some(Instant::now());
                    }
                }
            });

            let quit = MenuItem::with_id(app, "quit", "Выход", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&quit])?;
            #[cfg(target_os = "macos")]
            let icon = Image::from_bytes(include_bytes!("../../shared/icons/tray-template-44.png"))?;
            #[cfg(not(target_os = "macos"))]
            let icon = Image::from_bytes(include_bytes!("../../shared/icons/tray-32.png"))?;
            TrayIconBuilder::with_id("main")
                .icon(icon)
                .icon_as_template(cfg!(target_os = "macos"))
                .tooltip("Zatvor")
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, ev| {
                    if ev.id.as_ref() == "quit" {
                        app.exit(0);
                    }
                })
                .on_tray_icon_event(|tray, ev| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        rect,
                        ..
                    } = ev
                    {
                        toggle(tray.app_handle(), Some(rect));
                    }
                })
                .build(app)?;
            let tray_rect = app.tray_by_id("main").and_then(|t| t.rect().ok().flatten());
            log(&format!("tray created, rect {tray_rect:?}"));
            for m in app.available_monitors().unwrap_or_default() {
                log(&format!("monitor {:?} pos {:?} size {:?} work {:?} scale {}", m.name(), m.position(), m.size(), m.work_area(), m.scale_factor()));
            }

            if ui_check {
                let h = app.handle().clone();
                std::thread::spawn(move || ui_check_run(h, ui_rx));
            } else if !self_test {
                // Open once at start, so the app is visibly running.
                let h = app.handle().clone();
                std::thread::spawn(move || {
                    std::thread::sleep(Duration::from_millis(800));
                    let h2 = h.clone();
                    let _ = h.run_on_main_thread(move || toggle(&h2, None));
                });
            }
            if self_test {
                let h = app.handle().clone();
                std::thread::spawn(move || self_test_run(h, rx));
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("tauri run");
}

fn toggle(app: &AppHandle, rect: Option<Rect>) {
    log(&format!("toggle, click rect {rect:?}"));
    let Some(w) = app.get_webview_window("popup") else { return };
    if w.is_visible().unwrap_or(false) {
        log("popup visible: hide");
        let _ = w.hide();
        return;
    }
    // On Windows the click that lands on the tray icon first takes focus from
    // the popup (which hides it); do not reopen it in the same gesture.
    if let Some(t) = *app.state::<State>().last_blur_hide.lock().unwrap() {
        if t.elapsed() < Duration::from_millis(250) {
            log("click right after a focus-loss hide: stay hidden");
            return;
        }
    }
    place(&w, rect);
    let _ = w.show();
    let _ = w.set_focus();
    log(&format!("popup shown at {:?} size {:?}", w.outer_position(), w.outer_size()));
}

/// Put the popup next to the tray icon (see popup_position); without a tray
/// rect, centre it at the top of the primary monitor.
fn place(w: &WebviewWindow, rect: Option<Rect>) {
    let tray = rect.or_else(|| {
        w.app_handle()
            .tray_by_id("main")
            .and_then(|t| t.rect().ok().flatten())
    });
    let scale = w.scale_factor().unwrap_or(1.0);
    let tray = tray.map(|t| {
        let p: PhysicalPosition<f64> = t.position.to_physical(scale);
        let s: tauri::PhysicalSize<f64> = t.size.to_physical(scale);
        (p.x, p.y, s.width, s.height)
    });
    let mons: Vec<Mon> = w
        .available_monitors()
        .unwrap_or_default()
        .iter()
        .map(|m| Mon {
            x: m.position().x as f64,
            y: m.position().y as f64,
            w: m.size().width as f64,
            h: m.size().height as f64,
            wx: m.work_area().position.x as f64,
            wy: m.work_area().position.y as f64,
            ww: m.work_area().size.width as f64,
            wh: m.work_area().size.height as f64,
            scale: m.scale_factor(),
        })
        .collect();
    let primary = w.primary_monitor().ok().flatten().and_then(|p| {
        mons.iter().position(|m| m.x == p.position().x as f64 && m.y == p.position().y as f64)
    });
    match popup_position(tray, &mons, primary.unwrap_or(0)) {
        Some((x, y)) => {
            log(&format!("place: tray {tray:?} -> ({x}, {y})"));
            let _ = w.set_position(PhysicalPosition::new(x, y));
        }
        None => log(&format!("place: no monitor for tray {tray:?}; monitors {mons:?}")),
    }
}

/// A monitor in physical pixels: full bounds, work area, scale factor.
#[derive(Debug, Clone, Copy, PartialEq)]
struct Mon {
    x: f64,
    y: f64,
    w: f64,
    h: f64,
    wx: f64,
    wy: f64,
    ww: f64,
    wh: f64,
    scale: f64,
}

/// Top-left corner (physical pixels) for the popup next to a tray icon given
/// as (x, y, width, height) in physical pixels: above it for a bottom taskbar,
/// below it for a top bar (macOS menu bar), beside it for a left/right
/// taskbar; clamped to the work area. The monitor is the one whose physical
/// bounds hold the icon's centre (all values in one coordinate space, so a
/// Retina screen works: Tauri's monitor_from_point expects points on macOS).
fn popup_position(tray: Option<(f64, f64, f64, f64)>, mons: &[Mon], primary: usize) -> Option<(i32, i32)> {
    let inside = |m: &Mon, x: f64, y: f64| x >= m.x && x < m.x + m.w && y >= m.y && y < m.y + m.h;
    let Some((tx, ty, tw, th)) = tray else {
        let m = mons.get(primary).or(mons.first())?;
        let (pw, gap) = (W * m.scale, GAP * m.scale);
        return Some(((m.wx + m.ww - pw - gap).round() as i32, (m.wy + gap).round() as i32));
    };
    let (cx, cy) = (tx + tw / 2.0, ty + th / 2.0);
    let m = mons.iter().find(|m| inside(m, cx, cy))?;
    let (pw, ph, gap) = (W * m.scale, H * m.scale, GAP * m.scale);
    let (ax, ay, aw, ah) = (m.wx, m.wy, m.ww, m.wh);
    let (mut x, mut y);
    if cy >= ay + ah {
        // bottom taskbar
        x = cx - pw / 2.0;
        y = ay + ah - ph - gap;
    } else if ty + th <= ay {
        // top bar
        x = cx - pw / 2.0;
        y = ay + gap;
    } else if cx <= ax {
        x = ax + gap;
        y = cy - ph / 2.0;
    } else if cx >= ax + aw {
        x = ax + aw - pw - gap;
        y = cy - ph / 2.0;
    } else {
        // tray inside the work area (auto-hide taskbar): above or below it
        x = cx - pw / 2.0;
        y = if cy > ay + ah / 2.0 { ty - ph - gap } else { ty + th + gap };
    }
    // max/min instead of clamp: clamp panics when the area is smaller than the popup
    x = x.min(ax + aw - pw - gap).max(ax + gap);
    y = y.min(ay + ah - ph).max(ay);
    Some((x.round() as i32, y.round() as i32))
}

/// CI check of the interactive path: open the popup through toggle() (what a
/// tray click calls), check it lies on a monitor, click every control from the
/// page and wait for the page's reports, then write ui-check.json.
fn ui_check_run(app: AppHandle, rx: Receiver<String>) {
    let mut result = json!({"framework": "tauri", "os": std::env::consts::OS});
    let wait = |want: &str, secs: u64| -> bool {
        let end = Instant::now() + Duration::from_secs(secs);
        while let Some(left) = end.checked_duration_since(Instant::now()) {
            match rx.recv_timeout(left) {
                Ok(m) if m.starts_with(want) => return true,
                Ok(_) => continue,
                Err(_) => return false,
            }
        }
        false
    };
    result["pageLoaded"] = json!(wait("page loaded", 20));
    std::thread::sleep(Duration::from_secs(2));
    let tray_rect = app.tray_by_id("main").and_then(|t| t.rect().ok().flatten());
    result["trayRect"] = json!(format!("{tray_rect:?}"));
    let h = app.clone();
    let _ = app.run_on_main_thread(move || toggle(&h, None));
    std::thread::sleep(Duration::from_secs(1));
    let w = app.get_webview_window("popup").expect("popup");
    result["visible"] = json!(w.is_visible().unwrap_or(false));
    let pos = w.outer_position().ok();
    let size = w.outer_size().ok();
    result["popupPos"] = json!(pos.map(|p| [p.x, p.y]));
    result["popupSize"] = json!(size.map(|s| [s.width, s.height]));
    let on_screen = match (pos, size) {
        (Some(p), Some(s)) => app.available_monitors().unwrap_or_default().iter().any(|m| {
            let (mx, my) = (m.position().x, m.position().y);
            let (mw, mh) = (m.size().width as i32, m.size().height as i32);
            p.x >= mx && p.y >= my && p.x + s.width as i32 <= mx + mw && p.y + s.height as i32 <= my + mh
        }),
        _ => false,
    };
    result["onScreen"] = json!(on_screen);
    let mut clicks = serde_json::Map::new();
    for (sel, want) in [
        ("[data-action=power]", "click power"),
        ("[data-action=pause]", "click pause"),
        ("[data-action=check]", "click check"),
        ("[data-action=panel]", "click panel"),
        ("[data-action=menu]", "click menu"),
        ("[data-theme-set=light]", "click theme=light"),
        ("[data-theme-set=dark]", "click theme=dark"),
        ("[data-theme-set=system]", "click theme=system"),
    ] {
        let _ = w.eval(format!("document.querySelector('{sel}').click()"));
        clicks.insert(sel.to_string(), json!(wait(want, 5)));
        std::thread::sleep(Duration::from_millis(400));
    }
    result["clicks"] = Value::Object(clicks);
    if std::env::var_os("ZATVOR_UI_CHECK_HOLD").is_some() {
        // the workflow takes a screenshot meanwhile
        std::thread::sleep(Duration::from_secs(8));
    }
    result["log"] = json!(log_path().display().to_string());
    let exe = std::env::current_exe().unwrap_or_default();
    let out = exe.parent().map(|d| d.join("ui-check.json")).unwrap_or_else(|| PathBuf::from("ui-check.json"));
    let _ = std::fs::write(&out, serde_json::to_vec_pretty(&result).unwrap());
    log(&format!("ui-check done: {result}"));
    app.exit(0);
}

#[cfg(test)]
mod tests {
    use super::*;

    // MacBook Pro 14" Retina: 1512x982 points at scale 2, menu bar 37 points.
    fn retina() -> Mon {
        Mon { x: 0.0, y: 0.0, w: 3024.0, h: 1964.0, wx: 0.0, wy: 74.0, ww: 3024.0, wh: 1890.0, scale: 2.0 }
    }

    #[test]
    fn retina_menu_bar_icon_right_half() {
        // icon at x=1300 points: physical 2600, outside 1512 points (the old bug)
        let p = popup_position(Some((2600.0, 0.0, 64.0, 74.0)), &[retina()], 0).unwrap();
        assert_eq!(p, (2632 - 360, 74 + 16));
    }

    #[test]
    fn retina_icon_near_right_edge_is_clamped() {
        let p = popup_position(Some((2980.0, 0.0, 40.0, 74.0)), &[retina()], 0).unwrap();
        assert_eq!(p.0, 3024 - 720 - 16);
    }

    #[test]
    fn windows_bottom_taskbar() {
        let m = Mon { x: 0.0, y: 0.0, w: 1920.0, h: 1080.0, wx: 0.0, wy: 0.0, ww: 1920.0, wh: 1032.0, scale: 1.0 };
        let p = popup_position(Some((1700.0, 1040.0, 24.0, 32.0)), &[m], 0).unwrap();
        assert_eq!(p, (1712 - 180, 1032 - 560 - 8));
    }

    #[test]
    fn no_tray_rect_uses_top_right_of_primary() {
        assert_eq!(popup_position(None, &[retina()], 0), Some((3024 - 720 - 16, 74 + 16)));
    }

    #[test]
    fn tiny_work_area_does_not_panic() {
        let m = Mon { x: 0.0, y: 0.0, w: 300.0, h: 300.0, wx: 0.0, wy: 0.0, ww: 300.0, wh: 300.0, scale: 1.0 };
        assert!(popup_position(Some((100.0, 290.0, 10.0, 10.0)), &[m], 0).is_some());
    }

    #[test]
    fn icon_off_every_monitor() {
        assert_eq!(popup_position(Some((9000.0, 0.0, 10.0, 10.0)), &[retina()], 0), None);
    }
}

fn self_test_run(app: AppHandle, rx: Receiver<(u32, String, String, Instant)>) {
    let started = Instant::now();
    let mut notes: Vec<String> = Vec::new();
    std::thread::sleep(Duration::from_secs(60));
    let hidden = process_tree_memory();

    let w = app.get_webview_window("popup").expect("popup window");
    let mut samples = Vec::new();
    for i in 0..TOGGLES as u32 {
        while rx.try_recv().is_ok() {}
        let tray_rect = app.tray_by_id("main").and_then(|t| t.rect().ok().flatten());
        place(&w, tray_rect);
        let t0_wall = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(0);
        let t0 = Instant::now();
        let _ = w.show();
        let show_ret = t0.elapsed();
        let _ = w.eval(probe_js(i));
        let mut sample = json!({"id": i, "t0UnixNs": t0_wall, "showCallMs": ms(show_ret)});
        loop {
            match rx.recv_timeout(Duration::from_secs(5)) {
                Ok((id, ve, vr, at)) if id == i => {
                    sample["paintMs"] = json!(ms(at.duration_since(t0)));
                    sample["visAtEval"] = json!(ve);
                    sample["visAtRaf"] = json!(vr);
                    break;
                }
                Ok(_) => continue,
                Err(_) => {
                    sample["paintMs"] = Value::Null;
                    notes.push(format!("toggle {i}: no paint report within 5 s"));
                    break;
                }
            }
        }
        sample["visibleAfterShow"] = json!(w.is_visible().unwrap_or(false));
        samples.push(sample);
        std::thread::sleep(Duration::from_millis(300));
        let _ = w.hide();
        std::thread::sleep(Duration::from_millis(700));
    }
    let after = process_tree_memory();

    let mut paint: Vec<f64> = samples.iter().filter_map(|s| s["paintMs"].as_f64()).collect();
    paint.sort_by(|a, b| a.partial_cmp(b).unwrap());
    let open_ms = if paint.len() == TOGGLES { json!(median(&paint)) } else {
        json!(format!("not measured: {} of {TOGGLES} toggles reported a paint", paint.len()))
    };
    let exe = std::env::current_exe().unwrap_or_default();
    let mut m = json!({
        "framework": "tauri",
        "frameworkVersion": tauri::VERSION,
        "os": std::env::consts::OS,
        "arch": std::env::consts::ARCH,
        "timestampUnix": SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0),
        "method": "self-test: hidden 60 s, then 20 code toggles; openMs = show() call to the page's requestAnimationFrame callback reported over IPC (median)",
        "openMs": open_ms,
        "openMsFirstShow": samples.first().map(|s| s["paintMs"].clone()),
        "openMsMin": paint.first(),
        "openMsMax": paint.last(),
        "hiddenRssMB": hidden["rssMB"],
        "hiddenPrivateMB": hidden["privateMB"],
        "hiddenFootprintMB": hidden["footprintMB"],
        "childProcesses": hidden["processes"],
        "rssAfterTogglesMB": after["rssMB"],
        "processesAfterToggles": after["processes"],
        "binaryMB": mb(file_size(&exe)),
        "bundleMB": app_bundle(&exe).map(|b| mb(dir_size(&b))),
        "exe": exe.display().to_string(),
        "samples": samples,
        "selfTestSeconds": started.elapsed().as_secs(),
        "notes": notes,
    });
    if let Some(n) = hidden.get("note") {
        m["memoryNote"] = n.clone();
    }
    if let Some(n) = hidden.get("webkitProcessesSeen") {
        m["webkitProcessesSeen"] = n.clone();
    }
    let out = exe.parent().map(|d| d.join("metrics.json")).unwrap_or_else(|| PathBuf::from("metrics.json"));
    if let Err(e) = std::fs::write(&out, serde_json::to_vec_pretty(&m).unwrap()) {
        // e.g. macOS App Translocation runs a quarantined app from a read-only mount
        let home = std::env::var_os("HOME").or_else(|| std::env::var_os("USERPROFILE")).unwrap_or_default();
        let alt = PathBuf::from(home).join("zatvor-proto-tauri-metrics.json");
        m["metricsPathNote"] = json!(format!("could not write {}: {e}", out.display()));
        let _ = std::fs::write(&alt, serde_json::to_vec_pretty(&m).unwrap());
    }
    app.exit(0);
}

fn probe_js(id: u32) -> String {
    // The rAF callback runs for the first frame the page renders after show().
    format!(
        "(function(){{var ve=document.visibilityState;requestAnimationFrame(function(){{\
         window.__TAURI_INTERNALS__.invoke('painted',{{id:{id},visEval:ve,visRaf:document.visibilityState}});}});}})();"
    )
}

fn ms(d: Duration) -> f64 {
    (d.as_secs_f64() * 1000.0 * 10.0).round() / 10.0
}
fn mb(b: u64) -> f64 {
    (b as f64 / 1048576.0 * 10.0).round() / 10.0
}
fn median(v: &[f64]) -> f64 {
    let n = v.len();
    if n % 2 == 1 { v[n / 2] } else { ((v[n / 2 - 1] + v[n / 2]) / 2.0 * 10.0).round() / 10.0 }
}
fn file_size(p: &Path) -> u64 {
    std::fs::metadata(p).map(|m| m.len()).unwrap_or(0)
}
fn dir_size(p: &Path) -> u64 {
    let mut n = 0;
    if let Ok(rd) = std::fs::read_dir(p) {
        for e in rd.flatten() {
            let Ok(ft) = e.file_type() else { continue };
            if ft.is_dir() { n += dir_size(&e.path()) } else if ft.is_file() { n += file_size(&e.path()) }
        }
    }
    n
}
fn app_bundle(exe: &Path) -> Option<PathBuf> {
    exe.ancestors().find(|a| a.extension().map(|e| e == "app").unwrap_or(false)).map(Path::to_path_buf)
}

#[cfg(target_os = "macos")]
mod responsible {
    // WebKit's XPC helpers are children of launchd, but their "responsible"
    // process is the app that started them. The libSystem function is not in
    // a public header, so it is looked up at run time.
    use std::ffi::{c_char, c_void};
    extern "C" {
        fn dlsym(handle: *mut c_void, symbol: *const c_char) -> *mut c_void;
    }
    const RTLD_DEFAULT: *mut c_void = -2isize as *mut c_void;
    extern "C" {
        fn proc_pid_rusage(pid: i32, flavor: i32, buffer: *mut u64) -> i32;
    }
    /// phys_footprint (what Activity Monitor shows as Memory), from rusage_info_v2.
    pub fn footprint(pid: i32) -> Option<u64> {
        let mut buf = [0u64; 64];
        // RUSAGE_INFO_V2 = 2; ri_phys_footprint follows a 16-byte uuid and 7 u64s.
        if unsafe { proc_pid_rusage(pid, 2, buf.as_mut_ptr()) } == 0 { Some(buf[9]) } else { None }
    }
    pub fn lookup() -> Option<extern "C" fn(i32) -> i32> {
        let f = unsafe { dlsym(RTLD_DEFAULT, c"responsibility_get_pid_responsible_for_pid".as_ptr()) };
        if f.is_null() { None } else { Some(unsafe { std::mem::transmute::<*mut c_void, extern "C" fn(i32) -> i32>(f) }) }
    }
}

/// Working set (Windows) / resident size (macOS) of this process plus its
/// descendants by parent PID (WebView2 runs msedgewebview2.exe children). On
/// macOS, WKWebView helpers are attributed by responsible PID instead.
fn process_tree_memory() -> Value {
    use sysinfo::{Pid, ProcessRefreshKind, ProcessesToUpdate, System, UpdateKind};
    let Ok(mut sys) = System::new() else {
        return json!({"rssMB": "not measured: sysinfo unavailable", "processes": []});
    };
    sys.refresh_processes_specifics(
        ProcessesToUpdate::All,
        true,
        ProcessRefreshKind::nothing().with_memory().with_exe(UpdateKind::OnlyIfNotSet),
    );
    let me = Pid::from_u32(std::process::id());
    let mut kids: HashMap<Pid, Vec<Pid>> = HashMap::new();
    for (pid, p) in sys.processes() {
        if let Some(pp) = p.parent() {
            kids.entry(pp).or_default().push(*pid);
        }
    }
    let mut tree = vec![(me, "self")];
    let mut i = 0;
    while i < tree.len() {
        if let Some(c) = kids.get(&tree[i].0) {
            for k in c {
                if !tree.iter().any(|(p, _)| p == k) {
                    tree.push((*k, "child"));
                }
            }
        }
        i += 1;
    }
    #[cfg(not(target_os = "macos"))]
    let note: Option<&str> = None;
    #[cfg(target_os = "macos")]
    let note = if let Some(resp) = responsible::lookup() {
        for pid in sys.processes().keys() {
            if resp(pid.as_u32() as i32) == me.as_u32() as i32 && !tree.iter().any(|(p, _)| p == pid) {
                tree.push((*pid, "responsible"));
            }
        }
        Some("macOS: WebKit helpers are found by responsible PID (they are launchd children); resident size double-counts shared pages")
    } else {
        Some("macOS: responsibility_get_pid_responsible_for_pid not found; WebKit helper processes are NOT included")
    };
    let (mut rss, mut private) = (0u64, 0u64);
    #[allow(unused_mut)]
    let (mut foot, mut foot_ok) = (0u64, cfg!(target_os = "macos"));
    let mut list = Vec::new();
    for (pid, how) in &tree {
        if let Some(p) = sys.process(*pid) {
            rss += p.memory();
            private += p.virtual_memory();
            #[allow(unused_mut)]
            let mut e = json!({"pid": pid.as_u32(), "name": p.name().to_string_lossy(), "link": how,
                             "ppid": p.parent().map(|x| x.as_u32()), "rssMB": mb(p.memory())});
            #[cfg(target_os = "macos")]
            match responsible::footprint(pid.as_u32() as i32) {
                Some(f) => {
                    foot += f;
                    e["footprintMB"] = json!(mb(f));
                }
                None => foot_ok = false,
            }
            list.push(e);
        }
    }
    let mut v = json!({"rssMB": mb(rss), "processes": list});
    v["footprintMB"] = if foot_ok { json!(mb(foot)) } else if cfg!(target_os = "macos") {
        json!("not measured: proc_pid_rusage failed for a process in the tree")
    } else {
        json!("not measured: macOS only (see hiddenPrivateMB)")
    };
    #[cfg(target_os = "macos")]
    {
        // Every WebKit helper on the machine, for cross-checking the attribution.
        let resp = responsible::lookup();
        let seen: Vec<Value> = sys
            .processes()
            .iter()
            .filter(|(_, p)| p.name().to_string_lossy().contains("WebKit"))
            .map(|(pid, p)| json!({"pid": pid.as_u32(), "name": p.name().to_string_lossy(),
                "ppid": p.parent().map(|x| x.as_u32()), "responsible": resp.map(|f| f(pid.as_u32() as i32)),
                "rssMB": mb(p.memory())}))
            .collect();
        v["webkitProcessesSeen"] = json!(seen);
    }
    if cfg!(windows) {
        v["privateMB"] = json!(mb(private));
    } else {
        v["privateMB"] = json!("not measured: no private-bytes counter in sysinfo on this OS");
    }
    if let Some(n) = note {
        v["note"] = json!(n);
    }
    v
}
