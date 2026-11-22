package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/geo/assets"
	"github.com/ArtikGenius/zatvor/internal/geo/merge"
	"github.com/ArtikGenius/zatvor/internal/instance"
	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/safemode"
	"github.com/ArtikGenius/zatvor/internal/settings"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

const testLink = "vless://REPLACE_ME_uuid@reality.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.org&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=ab12&spx=%2F&type=tcp#r"

// ---- fakes: Xray runner, geo, system proxy ----

type fakeRunner struct {
	mu    sync.Mutex
	ev    chan xrayrun.Event
	stops int
}

func (r *fakeRunner) Start(ctx context.Context, cfg []byte, ready xrayrun.ReadyCheck) error {
	r.ev <- xrayrun.Event{Kind: xrayrun.Started}
	return nil
}

func (r *fakeRunner) Stop(time.Duration) error {
	r.mu.Lock()
	r.stops++
	r.mu.Unlock()
	return nil
}

func (r *fakeRunner) Events() <-chan xrayrun.Event { return r.ev }

type fakeGeo struct{}

func (fakeGeo) Prepare([]byte, *assets.ProfileGeo) (string, []merge.MissingTagError, error) {
	return "geo-set", nil, nil
}
func (fakeGeo) NeedsBootstrap([]byte, assets.ProfileGeo) bool { return false }
func (fakeGeo) Fetch(context.Context, assets.ProfileGeo, subfetch.Dialer) error {
	return nil
}

// fakeProxy is a SystemProxy over memory. check runs at every set.
type fakeProxy struct {
	mu    sync.Mutex
	lan   sysapi.ProxyState
	sets  []sysapi.ProxyState
	check func(sysapi.ProxyState)
}

func (p *fakeProxy) Get() (sysapi.ProxyState, error) { return p.GetConn("") }
func (p *fakeProxy) Set(s sysapi.ProxyState) error   { return p.SetConn("", s) }
func (p *fakeProxy) Connections() ([]string, error) {
	return nil, fmt.Errorf("fake: %w", sysapi.ErrRASUnavailable)
}

func (p *fakeProxy) GetConn(name string) (sysapi.ProxyState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name != "" {
		return sysapi.ProxyState{}, errors.New("fake: no such connection")
	}
	return p.lan, nil
}

func (p *fakeProxy) SetConn(name string, s sysapi.ProxyState) error {
	if p.check != nil {
		p.check(s)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if name != "" {
		return errors.New("fake: no such connection")
	}
	p.lan = s
	p.sets = append(p.sets, s)
	return nil
}

func (p *fakeProxy) LAN() sysapi.ProxyState { p.mu.Lock(); defer p.mu.Unlock(); return p.lan }

// ---- the rig: temp roots, a fake home, the daemon in a goroutine ----

type rig struct {
	t      *testing.T
	base   string
	dirs   appdir.Dirs
	home   string
	opts   Options
	runner *fakeRunner
	shows  chan struct{}
	addr   chan string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	dirs, err := appdir.Resolve(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	for _, f := range []struct{ path, body string }{
		{".bashrc", "# the user's bashrc\nalias ll='ls -l'\n"},
		{filepath.Join(".claude", "settings.json"), "{\n  \"model\": \"x\"\n}\n"},
		{filepath.Join("Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1"), "# the user's profile\r\n"},
		{filepath.Join("AppData", "Code", "User", "settings.json"), "{\n    // keep me\n    \"editor.tabSize\": 2\n}\n"},
	} {
		p := filepath.Join(home, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := &rig{t: t, base: base, dirs: dirs, home: home,
		runner: &fakeRunner{ev: make(chan xrayrun.Event, 64)},
		shows:  make(chan struct{}, 8), addr: make(chan string, 1)}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	r.opts = Options{
		Dirs:    dirs,
		Mode:    safemode.Safe,
		Version: "0.0.0-test",
		Env:     func(string) string { return "" },
		Log:     io.Discard,
		hooks: &hooks{
			lockName: testLockName(dirs, hex.EncodeToString(rnd[:])),
			roots: &devRoots{Home: home, Documents: filepath.Join(home, "Documents"),
				AppData: filepath.Join(home, "AppData")},
			machineID: "test-machine",
			engine: func(d *engine.Deps) {
				d.Assets = fakeGeo{}
				d.NewRunner = func(string) engine.Runner { return r.runner }
				d.XrayTest = func(context.Context, []byte, string) error { return nil }
				d.Ready = func(gen.Ports) xrayrun.ReadyCheck { return func(context.Context) error { return nil } }
				d.Checker = func(context.Context, subfetch.Dialer) engine.CheckResult {
					return engine.CheckResult{Status: engine.OK, IPv4: "203.0.113.9"}
				}
			},
			started: func(addr string) { r.addr <- addr },
			onShow:  func() { r.shows <- struct{}{} },
		},
	}
	return r
}

// testLockName is a lock of this test only: on Windows the mutex name is
// per user, not per data dir.
func testLockName(dirs appdir.Dirs, rnd string) string {
	if runtime.GOOS == "windows" {
		return "ZatvorTest-" + rnd
	}
	return instance.Name(dirs)
}

// seed stores one subscription with one link profile and returns its ID.
func (r *rig) seed() string {
	r.t.Helper()
	n, err := link.Parse(testLink)
	if err != nil {
		r.t.Fatal(err)
	}
	st, err := openStore(r.opts, func(string, ...any) {})
	if err != nil {
		r.t.Fatal(err)
	}
	sub := profile.Subscription{ID: "sub-1", URL: "https://sub.example.com/s/abc", SendDevice: true,
		Profiles: []profile.Profile{{ID: "prof-1", Name: "Test", SubscriptionID: "sub-1", Node: &n,
			RoutingSource: profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllExceptRU}}}}
	if err := st.Save([]profile.Subscription{sub}); err != nil {
		r.t.Fatal(err)
	}
	return "prof-1"
}

func (r *rig) saveSettings(edit func(*settings.Settings)) {
	r.t.Helper()
	s := settings.Defaults()
	edit(&s)
	if err := settings.Save(filepath.Join(r.dirs.Data, SettingsFile), s); err != nil {
		r.t.Fatal(err)
	}
}

type running struct {
	r      *rig
	cancel context.CancelFunc
	done   chan error
	addr   string
}

func (r *rig) start() *running {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, r.opts) }()
	select {
	case addr := <-r.addr:
		run := &running{r: r, cancel: cancel, done: done, addr: addr}
		r.t.Cleanup(func() { cancel(); <-done })
		return run
	case err := <-done:
		cancel()
		r.t.Fatalf("Run returned before the API started: %v", err)
	case <-time.After(20 * time.Second):
		cancel()
		r.t.Fatal("the daemon did not start")
	}
	return nil
}

func (run *running) stop() error {
	run.r.t.Helper()
	run.cancel()
	select {
	case err := <-run.done:
		run.done <- err // for the cleanup
		return err
	case <-time.After(30 * time.Second):
		run.r.t.Fatal("the daemon did not stop")
		return nil
	}
}

// call sends one API request with the daemon's token.
func (run *running) call(method, path string, body any) (int, []byte) {
	t := run.r.t
	t.Helper()
	tok, err := api.ReadToken(run.r.dirs)
	if err != nil {
		t.Fatal(err)
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+run.addr+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	c := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (run *running) connect(id string) {
	run.r.t.Helper()
	if st, body := run.call("POST", "/v1/connect", map[string]string{"profile": id}); st != 200 {
		run.r.t.Fatalf("connect: %d %s", st, body)
	}
}

func pending(t *testing.T, dir string) []journal.Entry {
	t.Helper()
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// snapshot hashes every file under root except those under skip.
func snapshot(t *testing.T, root, skip string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == skip {
			return filepath.SkipDir
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fi, _ := d.Info()
		sum := sha256.Sum256(b)
		out[p] = hex.EncodeToString(sum[:]) + " " + fi.ModTime().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func recorded(t *testing.T, dirs appdir.Dirs) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(dirs.Logs, SafeModeLog))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("safe-mode log line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

// ---- tests ----

func TestStartRevertsPendingJournal(t *testing.T) {
	r := newRig(t)
	prev := sysapi.ProxyState{Enabled: true, Server: "198.51.100.7:3128", Bypass: []string{"<local>"}}
	ours := sysproxy.Ours(31001)
	fp := &fakeProxy{lan: ours}
	fp.check = func(sysapi.ProxyState) {
		if _, err := os.Stat(filepath.Join(r.dirs.Run, api.DiscoveryFile)); err == nil {
			t.Error("the system proxy was restored after the API started")
		}
	}
	r.opts.hooks.sysProxy = fp
	j, err := journal.Open(filepath.Join(r.dirs.Journal, "safe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Begin(journal.Entry{Kind: sysproxy.Kind, Target: "lan", Prev: mustJSON(t, prev), Applied: mustJSON(t, ours)}); err != nil {
		t.Fatal(err)
	}

	run := r.start()
	if got := fp.LAN(); got.Server != prev.Server || !got.Enabled || len(got.Bypass) != 1 {
		t.Fatalf("LAN after start = %+v, want the journaled previous value %+v", got, prev)
	}
	if p := pending(t, filepath.Join(r.dirs.Journal, "safe")); len(p) != 0 {
		t.Fatalf("pending after start = %d entries, want 0", len(p))
	}
	if err := run.stop(); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestSafeStartLeavesLiveJournal(t *testing.T) {
	// R16: safe mode never opens or reverts the live journal.
	r := newRig(t)
	live := filepath.Join(r.dirs.Journal, "live")
	j, err := journal.Open(live)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Begin(journal.Entry{Kind: sysproxy.Kind, Target: "lan",
		Prev: mustJSON(t, sysapi.ProxyState{}), Applied: mustJSON(t, sysproxy.Ours(31001))}); err != nil {
		t.Fatal(err)
	}
	run := r.start()
	if err := run.stop(); err != nil {
		t.Fatal(err)
	}
	if p := pending(t, live); len(p) != 1 {
		t.Fatalf("live journal pending = %d, want 1 (untouched)", len(p))
	}
}

func TestSafeModeTouchesNothingOutsideDirs(t *testing.T) {
	r := newRig(t)
	id := r.seed()
	r.saveSettings(func(s *settings.Settings) { s.DevTools = true })
	before := snapshot(t, r.base, r.dirs.Data)

	run := r.start()
	run.connect(id)
	if st, body := run.call("POST", "/v1/devtools", map[string]bool{"on": true}); st != 200 {
		t.Fatalf("devtools on: %d %s", st, body)
	}
	waitState(t, r.dirs, func(s string) bool { return strings.HasPrefix(s, "http://127.0.0.1:") })
	if err := run.stop(); err != nil {
		t.Fatalf("Run = %v", err)
	}

	if after := snapshot(t, r.base, r.dirs.Data); fmt.Sprint(after) != fmt.Sprint(before) {
		for p, v := range after {
			if before[p] != v {
				t.Errorf("changed outside Dirs: %s", p)
			}
		}
		for p := range before {
			if _, ok := after[p]; !ok {
				t.Errorf("removed outside Dirs: %s", p)
			}
		}
	}

	var proxySet, fileWrite bool
	for _, m := range recorded(t, r.dirs) {
		op, _ := m["op"].(string)
		args, _ := m["args"].(map[string]any)
		switch op {
		case "proxy.set", "proxy.set_conn":
			st := args
			if s, ok := args["state"].(map[string]any); ok {
				st = s
			}
			if srv, _ := st["server"].(string); strings.HasPrefix(srv, "127.0.0.1:") && st["enabled"] == true {
				proxySet = true
			}
		case "file.write":
			if p, _ := args["path"].(string); strings.HasPrefix(p, r.home) {
				fileWrite = true
			}
		}
	}
	if !proxySet {
		t.Error("safe-mode.jsonl has no system proxy Set of our inbound")
	}
	if !fileWrite {
		t.Error("safe-mode.jsonl has no recorded dev-tool file write")
	}
}

// waitState waits until the devtools state file satisfies ok, and returns it.
func waitState(t *testing.T, dirs appdir.Dirs, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(dirs.Run, StateFileName))
		if ok(string(b)) {
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Fatalf("state file = %q", b)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExitRevertsAll(t *testing.T) {
	r := newRig(t)
	id := r.seed()
	user := sysapi.ProxyState{AutoDetect: true}
	fp := &fakeProxy{lan: user}
	r.opts.hooks.sysProxy = fp
	run := r.start()
	run.connect(id)
	if st, body := run.call("POST", "/v1/devtools", map[string]bool{"on": true}); st != 200 {
		t.Fatalf("devtools on: %d %s", st, body)
	}
	if got := fp.LAN(); !got.Enabled {
		t.Fatalf("LAN while connected = %+v, want ours", got)
	}
	waitState(t, r.dirs, func(s string) bool { return s != "" })
	if p := pending(t, filepath.Join(r.dirs.Journal, "safe")); len(p) == 0 {
		t.Fatal("nothing journaled while connected")
	}
	if err := run.stop(); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := fp.LAN(); got.Enabled || !got.AutoDetect {
		t.Fatalf("LAN after exit = %+v, want the user's %+v", got, user)
	}
	if p := pending(t, filepath.Join(r.dirs.Journal, "safe")); len(p) != 0 {
		t.Fatalf("pending after exit = %+v, want none", p)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dirs.Run, StateFileName)); len(b) != 0 {
		t.Fatalf("state file after exit = %q, want empty", b)
	}
	if _, err := os.Stat(filepath.Join(r.dirs.Run, api.DiscoveryFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("api.json after exit: %v", err)
	}
	r.runner.mu.Lock()
	stops := r.runner.stops
	r.runner.mu.Unlock()
	if stops == 0 {
		t.Fatal("Xray was not stopped on exit")
	}
}

func TestSecondStartForwards(t *testing.T) {
	r := newRig(t)
	r.start()
	second := r.opts
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Run(ctx, second); err != nil {
		t.Fatalf("second Run = %v, want nil after forwarding", err)
	}
	select {
	case <-r.shows:
	case <-time.After(5 * time.Second):
		t.Fatal("the running daemon got no show request")
	}
}

func TestLiveRefusedWithoutAllowLive(t *testing.T) {
	r := newRig(t)
	r.opts.Mode = safemode.Live
	r.opts.Env = func(string) string { return "" } // no ZATVOR_ALLOW_LIVE
	err := Run(context.Background(), r.opts)
	if !errors.Is(err, ErrLiveRefused) {
		t.Fatalf("Run in live mode without ZATVOR_ALLOW_LIVE = %v, want ErrLiveRefused", err)
	}
	if _, err := os.Stat(filepath.Join(r.dirs.Journal, "live")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a refused live start opened the live journal")
	}
	if _, err := os.Stat(filepath.Join(r.dirs.Run, api.DiscoveryFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a refused live start started the API")
	}
}

func TestConnectUnknownProfileNotFound(t *testing.T) {
	r := newRig(t)
	r.seed()
	run := r.start()
	st, body := run.call("POST", "/v1/connect", map[string]string{"profile": "nope"})
	if st != http.StatusNotFound || !strings.Contains(string(body), `"not_found"`) {
		t.Fatalf("connect unknown = %d %s, want 404 not_found", st, body)
	}
}

func TestPersistsLocalAPIPort(t *testing.T) {
	r := newRig(t)
	run := r.start()
	s, err := settings.Load(filepath.Join(r.dirs.Data, SettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := run.addr; fmt.Sprintf("127.0.0.1:%d", s.LocalAPIPort) != want {
		t.Fatalf("LocalAPIPort = %d, want the port of %s", s.LocalAPIPort, want)
	}
	for _, p := range []int{s.Ports.Mixed, s.Ports.Probe, s.Ports.DevTools, s.Ports.API} {
		if p == s.LocalAPIPort || p == 0 {
			t.Fatalf("Xray ports %+v vs local API port %d", s.Ports, s.LocalAPIPort)
		}
	}
}

func TestPutSettingsValidates(t *testing.T) {
	r := newRig(t)
	run := r.start()
	cur := func() settings.Settings {
		s, err := settings.Load(filepath.Join(r.dirs.Data, SettingsFile))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	base := cur()
	for name, edit := range map[string]func(*settings.Settings){
		"version 0":       func(s *settings.Settings) { s.Version = 0 },
		"version 2":       func(s *settings.Settings) { s.Version = 2 },
		"duplicate ports": func(s *settings.Settings) { s.Ports.Probe = s.Ports.Mixed },
		"port too big":    func(s *settings.Settings) { s.Ports.Mixed = 70000 },
		"bad log level":   func(s *settings.Settings) { s.LogLevel = "loud" },
		"api on xray":     func(s *settings.Settings) { s.LocalAPIPort = s.Ports.Mixed },
	} {
		s := base
		edit(&s)
		if st, body := run.call("PUT", "/v1/settings", s); st != http.StatusBadRequest || !strings.Contains(string(body), "bad_request") {
			t.Errorf("%s: %d %s, want 400 bad_request", name, st, body)
		}
	}
	if got := cur(); fmt.Sprint(got) != fmt.Sprint(base) {
		t.Fatalf("a refused PUT changed the file: %+v", got)
	}

	// Zero ports keep the current ones.
	s := base
	s.Ports = gen.Ports{}
	s.UpdateOnStart = true
	st, body := run.call("PUT", "/v1/settings", s)
	if st != 200 {
		t.Fatalf("PUT: %d %s", st, body)
	}
	if got := cur(); got.Ports != base.Ports || !got.UpdateOnStart {
		t.Fatalf("saved %+v, want the current ports and UpdateOnStart", got)
	}
}

func TestFanoutNeverBlocks(t *testing.T) {
	f := newFanout()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := f.subscribe(ctx)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10*fanoutBuffer; i++ {
			f.publish(api.StateEvent(engine.State{Phase: engine.Connecting}))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}
	n := 0
	for range slow {
		n++
	}
	if n > fanoutBuffer {
		t.Fatalf("a full subscriber got %d events, want it closed after %d", n, fanoutBuffer)
	}
	// A cancelled subscriber's channel is closed.
	c2, cancel2 := context.WithCancel(context.Background())
	ch := f.subscribe(c2)
	cancel2()
	select {
	case _, ok := <-ch:
		if ok {
			for range ch {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled subscription was not closed")
	}
}

func TestValidateDataDir(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	full := filepath.Join(base, "full")
	ours := filepath.Join(base, "ours")
	for _, d := range []string{home, full, filepath.Join(ours, "journal"), filepath.Join(ours, "run")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(full, "thesis.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.VolumeName(base) + string(filepath.Separator)
	for _, c := range []struct {
		path string
		ok   bool
	}{
		{"relative/dir", false},
		{root, false},
		{home, false},
		{full, false},
		{ours, true},
		{filepath.Join(base, "new"), true},
		{filepath.Join(home, "zatvor"), true},
	} {
		err := ValidateDataDir(c.path, home)
		if (err == nil) != c.ok {
			t.Errorf("ValidateDataDir(%q) = %v, want ok=%v", c.path, err, c.ok)
		}
	}
}

// manualClock fires After only when Advance passes its deadline.
type manualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []manualWaiter
}

type manualWaiter struct {
	at time.Time
	ch chan time.Time
}

func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, manualWaiter{c.now.Add(d), ch})
	return ch
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

func TestRewritesDiscoveryWhenRemoved(t *testing.T) {
	// K34: a cleaner that deletes the run files must not hide the daemon.
	r := newRig(t)
	clk := &manualClock{now: time.Unix(2_000_000, 0)}
	r.opts.Clock = clk
	run := r.start()
	disc := filepath.Join(r.dirs.Run, api.DiscoveryFile)
	if err := os.Remove(disc); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.dirs.Run, api.TokenFile)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		clk.Advance(discoveryCheck)
		if _, err := os.Stat(disc); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the discovery file was not written again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	d, err := api.ReadDiscovery(r.dirs)
	if err != nil {
		t.Fatal(err)
	}
	run.addr = fmt.Sprintf("127.0.0.1:%d", d.Port)
	if st, body := run.call("GET", "/v1/status", nil); st != 200 {
		t.Fatalf("status after the rewrite: %d %s", st, body)
	}
	if err := run.stop(); err != nil {
		t.Fatal(err)
	}
}
