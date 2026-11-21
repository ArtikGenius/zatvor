// Stage 0c tray + popup prototype, Tauri 2. Throwaway: see proto/ui/RESULTS.md.
//
// Tray icon (small iris); left click toggles a 360x560 borderless popup that is
// created hidden at start and placed next to the tray icon; the popup hides on
// focus loss. `--self-test` measures the budgets from spec §12 without a tray
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
fn painted(state: tauri::State<'_, State>, id: u32, vis_eval: String, vis_raf: String) {
    let now = Instant::now();
    let _ = state.painted_tx.lock().unwrap().send((id, vis_eval, vis_raf, now));
}

fn main() {
    let self_test = std::env::args().any(|a| a == "--self-test");
    let (tx, rx) = channel();

    tauri::Builder::default()
        .manage(State {
            painted_tx: Mutex::new(tx),
            last_blur_hide: Mutex::new(None),
            self_test,
        })
        .invoke_handler(tauri::generate_handler![painted])
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
                    if st.self_test {
                        return; // a runner may not give us focus; keep the popup up
                    }
                    if let Some(w) = handle.get_webview_window("popup") {
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
    let Some(w) = app.get_webview_window("popup") else { return };
    if w.is_visible().unwrap_or(false) {
        let _ = w.hide();
        return;
    }
    // On Windows the click that lands on the tray icon first takes focus from
    // the popup (which hides it); do not reopen it in the same gesture.
    if let Some(t) = *app.state::<State>().last_blur_hide.lock().unwrap() {
        if t.elapsed() < Duration::from_millis(250) {
            return;
        }
    }
    place(&w, rect);
    let _ = w.show();
    let _ = w.set_focus();
}

/// Put the popup next to the tray icon: above it for a bottom taskbar, below it
/// for a top bar (macOS menu bar), beside it for a left/right taskbar; clamp to
/// the monitor's work area.
fn place(w: &WebviewWindow, rect: Option<Rect>) {
    let tray = rect.or_else(|| {
        w.app_handle()
            .tray_by_id("main")
            .and_then(|t| t.rect().ok().flatten())
    });
    let Some(tray) = tray else { return };
    let scale = w.scale_factor().unwrap_or(1.0);
    let tp: PhysicalPosition<f64> = tray.position.to_physical(scale);
    let ts: tauri::PhysicalSize<f64> = tray.size.to_physical(scale);
    let (cx, cy) = (tp.x + ts.width / 2.0, tp.y + ts.height / 2.0);
    let Ok(Some(mon)) = w.monitor_from_point(cx, cy) else { return };
    let scale = mon.scale_factor();
    let (pw, ph, gap) = (W * scale, H * scale, GAP * scale);
    let wa = mon.work_area();
    let (ax, ay) = (wa.position.x as f64, wa.position.y as f64);
    let (aw, ah) = (wa.size.width as f64, wa.size.height as f64);
    let (mut x, mut y);
    if cy >= ay + ah {
        // bottom taskbar
        x = cx - pw / 2.0;
        y = ay + ah - ph - gap;
    } else if tp.y + ts.height <= ay {
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
        y = if cy > ay + ah / 2.0 { tp.y - ph - gap } else { tp.y + ts.height + gap };
    }
    x = x.clamp(ax + gap, ax + aw - pw - gap);
    y = y.clamp(ay, ay + ah - ph);
    let _ = w.set_position(PhysicalPosition::new(x.round() as i32, y.round() as i32));
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
