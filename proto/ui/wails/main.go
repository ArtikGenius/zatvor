//go:build windows || darwin

// Command wails is the stage 0c tray + popup prototype on Wails v3. Throwaway:
// see proto/ui/RESULTS.md.
//
// Tray icon (small iris); left click toggles a 360x560 frameless popup that is
// created hidden at start and attached to the tray icon (Wails positions it);
// the popup hides on focus loss. --self-test measures the budgets from spec §12
// without a tray click and writes metrics.json next to the executable.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/ArtikGenius/zatvor/proto/ui/shared"
)

const (
	popupW  = 360
	popupH  = 560
	gap     = 8
	toggles = 20
)

type paintReport struct {
	id       int
	visEval  string
	visRaf   string
	received time.Time
}

func main() {
	selfTest := slices.Contains(os.Args[1:], "--self-test")
	painted := make(chan paintReport, 64)

	app := application.New(application.Options{
		Name:        "ZatvorProtoWails",
		Description: "Stage 0c tray prototype",
		Assets:      application.AssetOptions{Handler: assets(painted)},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
		Windows: application.WindowsOptions{},
	})

	popup := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "popup",
		Title:           "Zatvor",
		Width:           popupW,
		Height:          popupH,
		Frameless:       true,
		AlwaysOnTop:     true,
		Hidden:          true,
		DisableResize:   true,
		HideOnEscape:    true,
		HideOnFocusLost: !selfTest, // a runner may not give us focus
		URL:             "/",
		Windows:         application.WindowsWindow{HiddenOnTaskbar: true},
	})

	tray := app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(mustRead("icons/tray-template-44.png"))
	} else {
		tray.SetIcon(mustRead("icons/tray-32.png"))
	}
	tray.SetTooltip("Zatvor")
	menu := app.NewMenu()
	menu.Add("Выход").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)
	tray.AttachWindow(popup).WindowOffset(gap)

	if selfTest {
		go func() {
			runSelfTest(tray, popup, painted)
			app.Quit()
		}()
	}
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func mustRead(name string) []byte {
	b, err := fs.ReadFile(shared.FS, name)
	if err != nil {
		panic(err)
	}
	return b
}

// assets serves the shared popup with the Wails runtime script added (ExecJS
// waits for the runtime's ready message) and takes paint reports from the probe.
func assets(painted chan<- paintReport) http.Handler {
	files := application.BundledAssetFileServer(shared.FS)
	page := strings.Replace(string(mustRead("popup.html")), "</head>",
		`<script type="module" src="/wails/runtime.js"></script>`+"\n</head>", 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/index.html", "/popup.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		case "/__painted":
			now := time.Now()
			q := r.URL.Query()
			id, _ := strconv.Atoi(q.Get("id"))
			select {
			case painted <- paintReport{id, q.Get("ve"), q.Get("vr"), now}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			files.ServeHTTP(w, r)
		}
	})
}

func probeJS(id int) string {
	// The rAF callback runs for the first frame the page renders after Show().
	return fmt.Sprintf(`(function(){var ve=document.visibilityState;requestAnimationFrame(function(){`+
		`fetch('/__painted?id=%d&ve='+ve+'&vr='+document.visibilityState,{cache:'no-store'});});})();`, id)
}

func runSelfTest(tray *application.SystemTray, popup *application.WebviewWindow, painted <-chan paintReport) {
	started := time.Now()
	var notes []string
	time.Sleep(60 * time.Second)
	hidden := processTreeMemory()

	var samples []map[string]any
	var paint []float64
	for i := range toggles {
		for len(painted) > 0 {
			<-painted
		}
		if err := tray.PositionWindow(popup, gap); err != nil {
			notes = append(notes, fmt.Sprintf("toggle %d: PositionWindow: %v", i, err))
		}
		t0 := time.Now()
		popup.Show()
		showRet := time.Since(t0)
		popup.ExecJS(probeJS(i))
		s := map[string]any{"id": i, "showCallMs": ms(showRet)}
		deadline := time.After(5 * time.Second)
	wait:
		for {
			select {
			case p := <-painted:
				if p.id != i {
					continue
				}
				d := ms(p.received.Sub(t0))
				s["paintMs"], s["visAtEval"], s["visAtRaf"] = d, p.visEval, p.visRaf
				paint = append(paint, d)
				break wait
			case <-deadline:
				s["paintMs"] = nil
				notes = append(notes, fmt.Sprintf("toggle %d: no paint report within 5 s", i))
				break wait
			}
		}
		s["visibleAfterShow"] = popup.IsVisible()
		samples = append(samples, s)
		time.Sleep(300 * time.Millisecond)
		popup.Hide()
		time.Sleep(700 * time.Millisecond)
	}
	after := processTreeMemory()

	slices.Sort(paint)
	var openMs any
	if len(paint) == toggles {
		openMs = median(paint)
	} else {
		openMs = fmt.Sprintf("not measured: %d of %d toggles reported a paint", len(paint), toggles)
	}
	exe, _ := os.Executable()
	m := map[string]any{
		"framework":             "wails",
		"frameworkVersion":      wailsVersion(),
		"os":                    runtime.GOOS,
		"arch":                  runtime.GOARCH,
		"timestampUnix":         time.Now().Unix(),
		"method":                "self-test: hidden 60 s, then 20 code toggles; openMs = Show() call to the page's requestAnimationFrame callback reported over the asset server (median)",
		"openMs":                openMs,
		"hiddenRssMB":           hidden.RSSMB,
		"hiddenPrivateMB":       hidden.PrivateMB,
		"childProcesses":        hidden.Processes,
		"rssAfterTogglesMB":     after.RSSMB,
		"processesAfterToggles": after.Processes,
		"binaryMB":              mb(fileSize(exe)),
		"exe":                   exe,
		"samples":               samples,
		"selfTestSeconds":       int(time.Since(started).Seconds()),
		"notes":                 notes,
	}
	if len(paint) > 0 {
		m["openMsMin"], m["openMsMax"] = paint[0], paint[len(paint)-1]
	}
	if b := appBundle(exe); b != "" {
		m["bundleMB"] = mb(dirSize(b))
	}
	if hidden.Note != "" {
		m["memoryNote"] = hidden.Note
	}
	if hidden.WebKitSeen != nil {
		m["webkitProcessesSeen"] = hidden.WebKitSeen
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	path := filepath.Join(filepath.Dir(exe), "metrics.json")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		// e.g. macOS App Translocation runs a quarantined app from a read-only mount
		home, _ := os.UserHomeDir()
		m["metricsPathNote"] = fmt.Sprintf("could not write %s: %v", path, err)
		out, _ = json.MarshalIndent(m, "", "  ")
		_ = os.WriteFile(filepath.Join(home, "zatvor-proto-wails-metrics.json"), out, 0o644)
	}
}

func wailsVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/wailsapp/wails/v3" {
				return d.Version
			}
		}
	}
	return "unknown"
}

type procInfo struct {
	PID   int     `json:"pid"`
	Name  string  `json:"name"`
	Link  string  `json:"link"`
	RSSMB float64 `json:"rssMB"`
	PPID  int     `json:"ppid,omitempty"`
	Resp  int     `json:"responsible,omitempty"`
}

type memReport struct {
	RSSMB      any
	PrivateMB  any
	Processes  []procInfo
	Note       string
	WebKitSeen []procInfo
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()/100) / 10 }
func mb(b uint64) float64        { return float64(b*10/1048576) / 10 }

func median(v []float64) float64 {
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return float64(int((v[n/2-1]+v[n/2])/2*10+0.5)) / 10
}

func fileSize(p string) uint64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return uint64(st.Size())
}

func dirSize(root string) uint64 {
	var n uint64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, e := d.Info(); e == nil {
				n += uint64(info.Size())
			}
		}
		return nil
	})
	return n
}

func appBundle(exe string) string {
	for d := filepath.Dir(exe); d != filepath.Dir(d); d = filepath.Dir(d) {
		if strings.HasSuffix(d, ".app") {
			return d
		}
	}
	return ""
}
