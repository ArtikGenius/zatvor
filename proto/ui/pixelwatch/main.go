//go:build windows

// Command pixelwatch measures, from outside the prototype, when the popup's
// pixels actually reach the screen. It is the same program for both
// frameworks, so it does not depend on how each one reports "painted".
//
//	pixelwatch -watch -title Zatvor -out pixels.jsonl -max 300s
//	pixelwatch -analyze -metrics metrics.json -pixels pixels.jsonl -out screen.json
//
// Watch mode reads one screen pixel inside the popup's window rectangle (near
// the bottom edge, plain page background) about every millisecond and logs a
// line whenever the colour or the window's visibility changes, with
// GetSystemTimePreciseAsFileTime timestamps. Analyze mode matches those lines
// with the self-test's show() timestamps (t0UnixNs in metrics.json samples).
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32         = windows.NewLazySystemDLL("user32.dll")
	gdi32          = windows.NewLazySystemDLL("gdi32.dll")
	pFindWindowW   = user32.NewProc("FindWindowW")
	pGetWindowRect = user32.NewProc("GetWindowRect")
	pGetDC         = user32.NewProc("GetDC")
	pReleaseDC     = user32.NewProc("ReleaseDC")
	pSetDPIAware   = user32.NewProc("SetProcessDpiAwarenessContext")
	pCreateCompDC  = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompBmp = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject  = gdi32.NewProc("SelectObject")
	pBitBlt        = gdi32.NewProc("BitBlt")
	pGetPixel      = gdi32.NewProc("GetPixel")
)

type rect struct{ L, T, R, B int32 }

type event struct {
	T       int64  `json:"t"` // unix ns
	Visible bool   `json:"vis"`
	RGB     uint32 `json:"rgb"`
	X       int32  `json:"x"`
	Y       int32  `json:"y"`
}

func now() int64 {
	var ft windows.Filetime
	windows.GetSystemTimePreciseAsFileTime(&ft)
	return ft.Nanoseconds()
}

func main() {
	watch := flag.Bool("watch", false, "watch the popup's pixels")
	analyze := flag.Bool("analyze", false, "match pixel log with metrics.json")
	title := flag.String("title", "Zatvor", "popup window title")
	out := flag.String("out", "", "output file")
	maxDur := flag.Duration("max", 300*time.Second, "watch at most this long")
	metrics := flag.String("metrics", "", "metrics.json from the self-test")
	pixels := flag.String("pixels", "", "pixel log from -watch")
	flag.Parse()
	switch {
	case *watch:
		runWatch(*title, *out, *maxDur)
	case *analyze:
		runAnalyze(*metrics, *pixels, *out)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func runWatch(title, out string, maxDur time.Duration) {
	// Per-monitor DPI aware v2, so that window rectangles are in screen pixels.
	pSetDPIAware.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	f, err := os.Create(out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	enc := json.NewEncoder(w)

	t16, _ := windows.UTF16PtrFromString(title)
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompDC.Call(screen)
	bmp, _, _ := pCreateCompBmp.Call(screen, 1, 1)
	pSelectObject.Call(mem, bmp)

	deadline := time.Now().Add(maxDur)
	var hwnd uintptr
	var seen bool
	var last event
	polls := 0
	start := time.Now()
	for time.Now().Before(deadline) {
		if hwnd == 0 {
			hwnd, _, _ = pFindWindowW.Call(0, uintptr(unsafe.Pointer(t16)))
			if hwnd == 0 {
				if seen {
					break // the prototype exited
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			seen = true
		}
		var r rect
		if ok, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
			hwnd = 0
			continue
		}
		x := (r.L + r.R) / 2
		y := r.B - (r.B-r.T)/20                                                   // bottom button row: page background
		pBitBlt.Call(mem, 0, 0, 1, 1, screen, uintptr(x), uintptr(y), 0x00CC0020) // SRCCOPY
		c, _, _ := pGetPixel.Call(mem, 0, 0)
		e := event{T: now(), Visible: windows.IsWindowVisible(windows.HWND(hwnd)), RGB: uint32(c), X: x, Y: y}
		polls++
		if e.Visible != last.Visible || e.RGB != last.RGB || e.X != last.X || e.Y != last.Y {
			_ = enc.Encode(e)
			last = e
		}
		time.Sleep(500 * time.Microsecond)
	}
	el := time.Since(start).Seconds()
	_ = enc.Encode(map[string]any{"polls": polls, "seconds": el, "pollMsAvg": el * 1000 / float64(max(polls, 1))})
}

type sample struct {
	ID       int   `json:"id"`
	T0UnixNs int64 `json:"t0UnixNs"`
}

func runAnalyze(metricsPath, pixelsPath, out string) {
	var m struct {
		Samples []sample `json:"samples"`
	}
	b, err := os.ReadFile(metricsPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		log.Fatal(err)
	}
	pf, err := os.Open(pixelsPath)
	if err != nil {
		log.Fatal(err)
	}
	defer pf.Close()
	var evs []event
	var tail map[string]any
	sc := bufio.NewScanner(pf)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.T != 0 {
			evs = append(evs, e)
		} else {
			_ = json.Unmarshal(sc.Bytes(), &tail)
		}
	}
	at := func(t int64) (event, bool) { // state at time t
		var cur event
		ok := false
		for _, e := range evs {
			if e.T > t {
				break
			}
			cur, ok = e, true
		}
		return cur, ok
	}
	var res []map[string]any
	var lat []float64
	for _, s := range m.Samples {
		r := map[string]any{"id": s.ID}
		res = append(res, r)
		if s.T0UnixNs == 0 {
			r["screenMs"] = "not measured: no t0UnixNs"
			continue
		}
		before, ok1 := at(s.T0UnixNs - 1)
		steady, ok2 := at(s.T0UnixNs + int64(250*time.Millisecond))
		if !ok1 || !ok2 || !steady.Visible {
			r["screenMs"] = "not measured: window not found or not visible 250 ms after show"
			continue
		}
		r["rgbBefore"], r["rgbShown"] = fmt.Sprintf("%06x", before.RGB), fmt.Sprintf("%06x", steady.RGB)
		if before.RGB == steady.RGB {
			r["screenMs"] = "not measured: popup pixel has the same colour as what was behind it"
			continue
		}
		for _, e := range evs {
			if e.T > s.T0UnixNs && e.RGB == steady.RGB {
				d := float64((e.T-s.T0UnixNs)/100000) / 10
				r["screenMs"] = d
				lat = append(lat, d)
				break
			}
		}
	}
	summary := map[string]any{"method": "external pixel watcher (proto/ui/pixelwatch): show() call timestamp to the first screen read with the popup's colour at a point inside the window; resolution about the poll interval",
		"samples": res, "watcher": tail}
	if len(res) > 0 {
		summary["screenMsFirstShow"] = res[0]["screenMs"]
	}
	if len(lat) > 0 {
		slices.Sort(lat)
		med := lat[len(lat)/2]
		if len(lat)%2 == 0 {
			med = float64(int((lat[len(lat)/2-1]+lat[len(lat)/2])/2*10+0.5)) / 10
		}
		summary["screenMsMedian"], summary["screenMsMin"], summary["screenMsMax"] = med, lat[0], lat[len(lat)-1]
		summary["measured"] = len(lat)
	} else {
		summary["screenMsMedian"] = "not measured: no sample matched"
	}
	o, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(out, o, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(o))
}
