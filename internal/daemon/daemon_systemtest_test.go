//go:build systemtest && windows

package daemon

// Live acceptance tests of zatvord on the disposable windows-latest runner:
// the daemon in live mode really sets the runner's system proxy and edits
// the runner's PowerShell profile. They run only behind the systest gate
// (CI=true and ZATVOR_ALLOW_LIVE=1, see internal/systest) and leave the
// runner as they found it: every test saves the runner's proxy state (LAN
// and RAS) and the files it may touch, and puts them back in t.Cleanup.
//
// The tunnel check runs against two local IP echoes (R6), passed in
// Options.CheckServices; nothing here talks to a third-party service.
//
// Processes: the in-process daemon needs none. The crash tests run the
// supervisor and the daemon as re-executions of this test binary (helper
// modes below, behind the same gate) so the daemon gets the local check
// services through Options, as no flag or environment variable can give
// them to zatvord. Only processes this test started are touched: the
// supervisor through its exec handle, the daemon child through one handle
// opened from the PID in its discovery file and verified (running, parent =
// our supervisor, created after the test started) before it is used
// (ruling R25).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/devtools"
	"github.com/ArtikGenius/zatvor/internal/e2e"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/instance"
	"github.com/ArtikGenius/zatvor/internal/safemode"
	"github.com/ArtikGenius/zatvor/internal/store"
	"github.com/ArtikGenius/zatvor/internal/supervisor"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/systest"
)

// TestMain runs the helper modes, else the tests behind the systest gate.
func TestMain(m *testing.M) {
	if code, ok := liveHelper(); ok {
		os.Exit(code)
	}
	systest.Main(m)
}

// Helper modes: `<test binary> <mode> <config.json>`.
const (
	helperChildArg      = "zatvor-systemtest-daemon"     // the daemon, until stdin closes
	helperSupervisorArg = "zatvor-systemtest-supervisor" // supervisor.Run over helperChildArg
)

// helperConfig is what a helper process runs with.
type helperConfig struct {
	Data   string   `json:"data"` // the data directory
	Xray   string   `json:"xray"`
	Geo    string   `json:"geo"`
	Lock   string   `json:"lock"` // the instance lock name of this test
	Checks []string `json:"checks"`
	// MinRestart, when set, is the helper supervisor's shortest restart
	// backoff (see minDelayClock).
	MinRestart time.Duration `json:"minRestart"`
}

const (
	liveVersion  = "0.0.0-systemtest"
	startTimeout = 60 * time.Second // a daemon start, a connect
	stopWait     = 60 * time.Second // a clean stop (the supervisor gives its child 30 s)
)

func liveHelper() (int, bool) {
	if len(os.Args) != 3 || (os.Args[1] != helperChildArg && os.Args[1] != helperSupervisorArg) {
		return 0, false
	}
	// The same gate as systest.Main: a helper changes the host too.
	if os.Getenv("CI") != "true" || os.Getenv("ZATVOR_ALLOW_LIVE") != "1" {
		fmt.Fprintln(os.Stderr, systest.Refusal)
		return systest.ExitRefused, true
	}
	raw, err := os.ReadFile(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return 2, true
	}
	var cfg helperConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return 2, true
	}
	opts, err := cfg.options(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return 2, true
	}
	ctx, cancel := stdinContext()
	defer cancel()
	if os.Args[1] == helperChildArg {
		if err := Run(ctx, opts); err != nil {
			fmt.Fprintln(os.Stderr, "helper daemon:", err)
			return 1, true
		}
		return 0, true
	}
	return runHelperSupervisor(ctx, opts, cfg.Lock, minDelayClock{cfg.MinRestart}), true
}

// minDelayClock is the wall clock whose After waits at least min. The
// supervisor uses its clock's After only for the restart backoff, so this
// stretches the backoff and nothing else.
type minDelayClock struct{ min time.Duration }

func (minDelayClock) Now() time.Time { return time.Now() }

func (c minDelayClock) After(d time.Duration) <-chan time.Time { return time.After(max(d, c.min)) }

// stdinContext ends when stdin closes: how a supervisor stops its child
// and how the test stops its supervisor (as cmd/zatvord).
func stdinContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	return ctx, cancel
}

// runHelperSupervisor is cmd/zatvord's supervisor over helper children:
// the same supervisor.Run with the journal of the live mode, its reverters
// and the instance lock before every revert.
func runHelperSupervisor(ctx context.Context, opts Options, lockName string, clock minDelayClock) int {
	rv, err := Reverters(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper supervisor:", err)
		return 1
	}
	defer rv.Close()
	child := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], helperChildArg, os.Args[2])
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd
	}
	lock := func() (func(), bool) {
		rel, held, err := instance.Acquire(lockName)
		if err != nil || held {
			return func() {}, false
		}
		return rel, true
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "helper "+format+"\n", a...) }
	if err := supervisor.Run(ctx, child, rv.Journal, rv.Reverters, clock, supervisor.WithRevertLock(lock),
		supervisor.WithLogf(logf), supervisor.WithAfterRevert(rv.Cleanup)); err != nil {
		fmt.Fprintln(os.Stderr, "helper supervisor:", err)
		return 1
	}
	return 0
}

// options are the live daemon's Options for cfg: the runner's real system
// proxy and user directories, this test's data directory, lock and check
// services.
func (cfg helperConfig) options(log io.Writer) (Options, error) {
	dirs, err := appdir.Resolve(cfg.Data)
	if err != nil {
		return Options{}, err
	}
	return Options{
		Dirs:          dirs,
		Mode:          safemode.Live,
		XrayPath:      cfg.Xray,
		BundledGeo:    cfg.Geo,
		Version:       liveVersion,
		Log:           log,
		CheckServices: cfg.Checks,
		hooks:         &hooks{lockName: cfg.Lock},
	}, nil
}

// ---- the live environment of one test ----

type liveEnv struct {
	t       *testing.T
	start   time.Time // processes of this test are created after it
	cfg     helperConfig
	cfgPath string
	dirs    appdir.Dirs
	srv     *liveServers
	logPath string // the daemon's (and helpers') log, shown when the test fails
}

func newLiveEnv(t *testing.T) *liveEnv { return newLiveEnvWith(t, 0) }

// newLiveEnvWith is newLiveEnv whose helper supervisor waits at least
// minRestart before it restarts a crashed daemon.
func newLiveEnvWith(t *testing.T, minRestart time.Duration) *liveEnv {
	t.Helper()
	xray := os.Getenv("ZATVOR_XRAY")
	if xray == "" {
		t.Fatal("ZATVOR_XRAY is not set (the CI job sets it to the pinned Xray)")
	}
	geo, err := filepath.Abs(filepath.Join("..", "..", "third_party", "geo", "files"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(geo, "sources.json")); err != nil {
		t.Fatalf("bundled geo (tools/geofetch): %v", err)
	}
	tmp := t.TempDir()
	dirs, err := appdir.Resolve(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	// Registered first, so it runs last: after every daemon has stopped.
	t.Cleanup(func() {
		if err := store.DeleteCredManKey(credTarget(dirs)); err != nil {
			t.Errorf("cleanup: delete the live store's Credential Manager key: %v", err)
		}
	})
	rnd, err := e2e.RandomHex(8)
	if err != nil {
		t.Fatal(err)
	}
	e := &liveEnv{t: t, start: time.Now().Add(-time.Second), dirs: dirs,
		cfgPath: filepath.Join(tmp, "helper.json"), logPath: filepath.Join(tmp, "daemon.log")}
	t.Cleanup(func() {
		if t.Failed() {
			b, _ := os.ReadFile(e.logPath)
			t.Logf("daemon log:\n%s", b)
			if e.srv != nil {
				t.Logf("reality server logs:\n%s", e.srv.Reality.Logs())
			}
		}
	})
	e.srv = startLiveServers(t, xray)
	e.cfg = helperConfig{Data: dirs.Data, Xray: xray, Geo: geo, Lock: "ZatvorSystemtest-" + rnd, Checks: e.srv.Checks,
		MinRestart: minRestart}
	b, err := json.Marshal(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *liveEnv) openLog() *os.File {
	e.t.Helper()
	f, err := os.OpenFile(e.logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

// runInProcess runs the live daemon in this test process until the
// returned stop (also called in t.Cleanup) ends it.
func (e *liveEnv) runInProcess() (stop func() error) {
	t := e.t
	t.Helper()
	logf := e.openLog()
	opts, err := e.cfg.options(logf)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 1)
	opts.hooks.started = func(addr string) { started <- addr }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	var result error
	stopped := false
	stop = func() error {
		if !stopped {
			stopped = true
			cancel()
			select {
			case result = <-done:
			case <-time.After(stopWait):
				result = errors.New("the daemon did not stop")
			}
			logf.Close()
		}
		return result
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("cleanup: stop the daemon: %v", err)
		}
	})
	select {
	case <-started:
	case err := <-done:
		done <- err
		t.Fatalf("the daemon returned before its API started: %v", err)
	case <-time.After(startTimeout):
		t.Fatal("the daemon's API did not start")
	}
	return stop
}

// helperProc is a helper process this test started.
type helperProc struct {
	cmd    *exec.Cmd
	stdin  io.Closer
	done   chan struct{}
	err    error // cmd.Wait's, read after done
	killed bool  // by kill: its exit error is expected
}

// startSupervisor starts the helper supervisor (which starts the helper
// daemon). Its stop closes the supervisor's stdin; t.Cleanup stops it and,
// should it hang, kills it through its handle.
func (e *liveEnv) startSupervisor() *helperProc {
	t := e.t
	t.Helper()
	logf := e.openLog()
	defer logf.Close() // the child has its own handle
	cmd := exec.Command(os.Args[0], helperSupervisorArg, e.cfgPath)
	cmd.Stdout, cmd.Stderr = logf, logf
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &helperProc{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		if err := p.stop(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return p
}

// stop asks the supervisor to stop cleanly and waits; a supervisor that
// hangs is killed. It returns the exit error.
func (p *helperProc) stop() error {
	p.stdin.Close()
	select {
	case <-p.done:
		if p.killed {
			return nil
		}
		return p.err
	case <-time.After(stopWait):
	}
	_ = p.cmd.Process.Kill() // our own child, by its handle
	<-p.done
	return errors.New("the supervisor did not stop; killed")
}

// kill terminates the supervisor at once, as a power loss or a tree kill
// would: nothing of it runs afterwards.
func (p *helperProc) kill(t *testing.T) {
	t.Helper()
	p.killed = true
	if err := p.cmd.Process.Kill(); err != nil { // TerminateProcess through our handle
		t.Fatalf("kill the supervisor: %v", err)
	}
	<-p.done
}

// ---- the local API ----

// call sends one request to the daemon's API with its token. It re-reads
// the discovery file every time, as the daemon may have restarted.
func (e *liveEnv) call(method, path string, body any) (int, []byte, error) {
	d, err := api.ReadDiscovery(e.dirs)
	if err != nil {
		return 0, nil, err
	}
	tok, err := api.ReadToken(e.dirs)
	if err != nil {
		return 0, nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(d.Port)+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	c := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: startTimeout}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return resp.StatusCode, out, err
}

// mustCall is call with want as the only accepted status.
func (e *liveEnv) mustCall(method, path string, body any, want int) []byte {
	e.t.Helper()
	st, out, err := e.call(method, path, body)
	if err != nil || st != want {
		e.t.Fatalf("%s %s = %d %s, %v; want %d", method, path, st, out, err, want)
	}
	return out
}

// waitDaemon waits until a daemon whose PID is not notPID answers the
// API, and returns its PID (from its discovery file).
func (e *liveEnv) waitDaemon(notPID int) int {
	e.t.Helper()
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if d, err := api.ReadDiscovery(e.dirs); err == nil && d.PID != notPID && d.PID > 0 {
			if st, _, err := e.call("GET", "/v1/status", nil); err == nil && st == http.StatusOK {
				if again, err := api.ReadDiscovery(e.dirs); err == nil && again == d {
					return d.PID
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("no daemon (other than pid %d) answers the API", notPID)
	return 0
}

// addSubscription adds the subscription through the API and returns the
// ID of the VLESS Reality profile.
func (e *liveEnv) addSubscription() string {
	e.t.Helper()
	e.mustCall("POST", "/v1/subscriptions", map[string]string{"url": e.srv.SubURL}, http.StatusOK)
	if n := e.srv.SubHits.Load(); n != 1 {
		e.t.Fatalf("the subscription server got %d requests, want 1", n)
	}
	var list struct{ Profiles []api.ProfileInfo }
	if err := json.Unmarshal(e.mustCall("GET", "/v1/profiles", nil, http.StatusOK), &list); err != nil {
		e.t.Fatal(err)
	}
	id := ""
	names := map[string]bool{}
	for _, p := range list.Profiles {
		names[p.Name] = true
		if p.Name == liveRealityName && p.Protocol == "vless" {
			id = p.ID
		}
	}
	if id == "" || !names[liveHy2Name] || len(list.Profiles) != 2 {
		e.t.Fatalf("profiles after adding the subscription: %+v; want the Reality and the Hysteria2 profile", list.Profiles)
	}
	return id
}

func decodeState(t *testing.T, b []byte) engine.State {
	t.Helper()
	var st engine.State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("state %s: %v", b, err)
	}
	return st
}

// connect connects the profile and returns the connected state.
func (e *liveEnv) connect(id string) engine.State {
	e.t.Helper()
	st := decodeState(e.t, e.mustCall("POST", "/v1/connect", map[string]string{"profile": id}, http.StatusOK))
	e.requireConnected(st)
	return st
}

func (e *liveEnv) requireConnected(st engine.State) {
	e.t.Helper()
	if st.Phase != engine.Connected || st.Check.Status != engine.OK || st.Ports.Mixed == 0 {
		e.t.Fatalf("state after connect: %+v; want connected with check ok", st)
	}
	e.t.Logf("connected: mixed %d, probe %d, dev tools %d; exit %s", st.Ports.Mixed, st.Ports.Probe, st.Ports.DevTools, st.Check.IPv4)
}

// ---- the runner's system proxy ----

// saveRunner saves the runner's LAN and RAS proxy state and puts it back in
// t.Cleanup, which also runs when the test fails.
func saveRunner(t *testing.T, sp sysapi.SystemProxy) map[string]sysapi.ProxyState {
	t.Helper()
	names, err := sp.Connections()
	if err != nil {
		t.Fatalf("Connections = %v", err)
	}
	saved := map[string]sysapi.ProxyState{}
	for _, name := range append([]string{""}, names...) {
		s, err := sp.GetConn(name)
		if err != nil {
			t.Fatalf("GetConn(%q) = %v", name, err)
		}
		saved[name] = s
	}
	t.Logf("runner proxy state: %d RAS entries, LAN %s", len(names), mustJSON(t, saved[""]))
	t.Cleanup(func() {
		for name, s := range saved {
			if err := sp.SetConn(name, s); err != nil {
				t.Errorf("cleanup: restore %q: %v", name, err)
			}
		}
		if got, err := sp.Get(); err != nil || !reflect.DeepEqual(got, saved[""]) {
			t.Errorf("cleanup: LAN = %+v, %v; want %+v", got, err, saved[""])
		}
	})
	return saved
}

// requireRunner asserts every connection holds its saved state exactly.
func requireRunner(t *testing.T, sp sysapi.SystemProxy, saved map[string]sysapi.ProxyState) {
	t.Helper()
	for name, want := range saved {
		got, err := sp.GetConn(name)
		if err != nil {
			t.Fatalf("GetConn(%q) = %v", name, err)
		}
		if d := stateDiff(got, want); d != "" {
			t.Fatalf("connection %q is not back to the runner's state: %s", name, d)
		}
	}
}

// stateDiff compares two proxy states field by field; "" when equal.
func stateDiff(got, want sysapi.ProxyState) string {
	var d []string
	if got.Enabled != want.Enabled {
		d = append(d, fmt.Sprintf("Enabled %v, want %v", got.Enabled, want.Enabled))
	}
	if got.Server != want.Server {
		d = append(d, fmt.Sprintf("Server %q, want %q", got.Server, want.Server))
	}
	if !reflect.DeepEqual(got.Bypass, want.Bypass) && (len(got.Bypass) != 0 || len(want.Bypass) != 0) {
		d = append(d, fmt.Sprintf("Bypass %q, want %q", got.Bypass, want.Bypass))
	}
	if got.PACURL != want.PACURL {
		d = append(d, fmt.Sprintf("PACURL %q, want %q", got.PACURL, want.PACURL))
	}
	if got.AutoDetect != want.AutoDetect {
		d = append(d, fmt.Sprintf("AutoDetect %v, want %v", got.AutoDetect, want.AutoDetect))
	}
	return strings.Join(d, "; ")
}

// waitLAN waits until the LAN settings equal want exactly.
func waitLAN(t *testing.T, sp sysapi.SystemProxy, want sysapi.ProxyState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got, err := sp.Get()
		if err == nil && stateDiff(got, want) == "" {
			return
		}
		if time.Now().After(deadline) {
			d := ""
			if err == nil {
				d = stateDiff(got, want)
			}
			t.Fatalf("LAN not restored within %s: %s (err %v)", timeout, d, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

const internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// registryProxy reads the manual proxy from the registry, as programs that
// do not use WinINet see it.
func registryProxy(t *testing.T) (enabled bool, server string) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open Internet Settings: %v", err)
	}
	defer k.Close()
	en, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("ProxyEnable: %v", err)
	}
	server, _, err = k.GetStringValue("ProxyServer")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("ProxyServer: %v", err)
	}
	return en == 1, server
}

// ---- processes ----

// openVerified opens pid and keeps the handle only if, read through that
// same handle, the process still runs, its parent is wantParent and it was
// created at or after notBefore. A PID that was reused fails the check, so
// the test never acts on a process it did not start (R25).
func openVerified(pid, wantParent int, notBefore time.Time) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	fail := func(err error) (windows.Handle, error) { windows.CloseHandle(h); return 0, err }
	if ev, err := windows.WaitForSingleObject(h, 0); err != nil || ev != uint32(windows.WAIT_TIMEOUT) {
		return fail(fmt.Errorf("not running (wait=%#x err=%v)", ev, err))
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return fail(fmt.Errorf("GetProcessTimes: %w", err))
	}
	if c := time.Unix(0, created.Nanoseconds()); c.Before(notBefore) {
		return fail(fmt.Errorf("created %v, before the test started (%v)", c, notBefore))
	}
	var pbi windows.PROCESS_BASIC_INFORMATION
	var n uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &n); err != nil {
		return fail(fmt.Errorf("NtQueryInformationProcess: %w", err))
	}
	if got := int(pbi.InheritedFromUniqueProcessId); got != wantParent {
		return fail(fmt.Errorf("parent is %d, want our supervisor %d", got, wantParent))
	}
	return h, nil
}

// killChild terminates the supervisor's daemon child pid through a
// verified handle and waits until it is gone.
func (e *liveEnv) killChild(pid int, sup *helperProc) {
	e.t.Helper()
	h, err := openVerified(pid, sup.cmd.Process.Pid, e.start)
	if err != nil {
		e.t.Fatalf("daemon child %d: %v", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		e.t.Fatalf("TerminateProcess(daemon child %d): %v", pid, err)
	}
	if ev, err := windows.WaitForSingleObject(h, 15_000); err != nil || ev != windows.WAIT_OBJECT_0 {
		e.t.Fatalf("daemon child %d still runs after TerminateProcess (wait=%#x err=%v)", pid, ev, err)
	}
}

// ---- the tests ----

// TestLiveDaemonEndToEnd: the live daemon in a temp data dir connects the
// VLESS Reality profile of a subscription added through the API, sets the
// system proxy to its mixed inbound, carries a request through the Reality
// server, passes `zatvor check`, and on disconnect gives the runner its
// proxy state back.
func TestLiveDaemonEndToEnd(t *testing.T) {
	systest.Require(t)
	sp := sysproxy.NewWindows()
	saved := saveRunner(t, sp)
	e := newLiveEnv(t)
	stop := e.runInProcess()

	id := e.addSubscription()
	st := e.connect(id)

	// The registry proxy is our mixed inbound.
	ours := sysproxy.Ours(st.Ports.Mixed)
	enabled, server := registryProxy(t)
	if !enabled || server != ours.Server {
		t.Fatalf("registry proxy = enabled %v, %q; want enabled, %q", enabled, server, ours.Server)
	}

	// A client that does not use WinINet, configured from the registry,
	// reaches the echo through the Reality server.
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: server}), DisableKeepAlives: true},
		Timeout:   15 * time.Second,
	}
	resp, err := client.Get("http://" + e.srv.Echo.Addr + "/ip")
	if err != nil {
		t.Fatalf("GET through the registry proxy: %v", err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET through the registry proxy: %d %q, %v", resp.StatusCode, b, err)
	}
	seen := strings.TrimSpace(string(b))
	// e2e gives the servers no sendThrough address on Windows, so the
	// access logs prove the path (R54): the Reality server carried the
	// request and the Hysteria2 server did not.
	t.Logf("echo saw %s", seen)
	if err := e.srv.Reality.WaitCarried(e.srv.Echo.Addr, 5*time.Second); err != nil {
		t.Fatalf("reality server: %v", err)
	}
	if err := e.srv.Hy2.WaitCarried(e.srv.Echo.Addr, 0); err == nil {
		t.Fatal("the Hysteria2 server carried the request; want only the Reality server")
	}

	// zatvor check, the real binary, against this data directory.
	cli := buildCLI(t)
	out, err := exec.Command(cli, "check", "--data-dir", e.dirs.Data).CombinedOutput()
	if err != nil {
		t.Fatalf("zatvor check: %v\n%s", err, out)
	}
	t.Logf("zatvor check (exit 0):\n%s", out)

	e.mustCall("POST", "/v1/disconnect", nil, http.StatusOK)
	requireRunner(t, sp, saved)
	if err := stop(); err != nil {
		t.Fatalf("daemon exit: %v", err)
	}
	requireRunner(t, sp, saved)
}

// buildCLI builds cmd/zatvor into a temp dir.
func buildCLI(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool not found: %v", err)
	}
	out := filepath.Join(t.TempDir(), "zatvor.exe")
	if b, err := exec.Command(goBin, "build", "-o", out, "github.com/ArtikGenius/zatvor/cmd/zatvor").CombinedOutput(); err != nil {
		t.Fatalf("build cmd/zatvor: %v\n%s", err, b)
	}
	return out
}

// foreignPAC is another program's proxy setting: a PAC URL, auto-detect,
// and a stored but disabled manual server with a bypass list.
var foreignPAC = sysapi.ProxyState{
	Server:     "192.0.2.10:3128", // TEST-NET-1, stored but not enabled
	Bypass:     []string{"z.example.test", "<local>", "a.example.test"},
	PACURL:     "http://127.0.0.1:9/zatvor-systemtest.pac",
	AutoDetect: true,
}

// connectOverForeign sets foreignPAC on the runner, connects (which must
// stop at proxy_conflict and change nothing) and forces the connect. It
// returns the connected state.
func (e *liveEnv) connectOverForeign(sp sysapi.SystemProxy) engine.State {
	t := e.t
	t.Helper()
	if err := sp.Set(foreignPAC); err != nil {
		t.Fatalf("Set(foreign PAC) = %v", err)
	}
	waitLAN(t, sp, foreignPAC, 5*time.Second)
	id := e.addSubscription()
	st, body, err := e.call("POST", "/v1/connect", map[string]string{"profile": id})
	if err != nil || st != http.StatusConflict || !bytes.Contains(body, []byte(`"code":"`+engine.CodeProxyConflict+`"`)) {
		t.Fatalf("connect over a foreign PAC = %d %s, %v; want 409 %s", st, body, err, engine.CodeProxyConflict)
	}
	waitLAN(t, sp, foreignPAC, 0)
	state := decodeState(t, e.mustCall("POST", "/v1/proxy/resolve-conflict", map[string]bool{"force": true}, http.StatusOK))
	e.requireConnected(state)
	waitLAN(t, sp, sysproxy.Ours(state.Ports.Mixed), 0)
	return state
}

// TestLiveCrashRevert (Review Focus 1): the daemon is killed while the
// system proxy is ours over a foreign PAC; the next start restores the PAC
// exactly.
func TestLiveCrashRevert(t *testing.T) {
	systest.Require(t)

	t.Run("supervisor restarts the killed daemon", func(t *testing.T) {
		sp := sysproxy.NewWindows()
		saveRunner(t, sp)
		e := newLiveEnv(t)
		sup := e.startSupervisor()
		pid := e.waitDaemon(0)
		e.connectOverForeign(sp)

		e.killChild(pid, sup)
		next := e.waitDaemon(pid)
		h, err := openVerified(next, sup.cmd.Process.Pid, e.start)
		if err != nil {
			t.Fatalf("restarted daemon %d: %v", next, err)
		}
		windows.CloseHandle(h)
		// The restart's RevertAll ran before its API started.
		waitLAN(t, sp, foreignPAC, 0)

		if err := sup.stop(); err != nil {
			t.Fatalf("supervisor exit: %v", err)
		}
		waitLAN(t, sp, foreignPAC, 0)
		requireNoProxyEntries(t, e.dirs)
	})

	t.Run("supervisor and daemon killed, fresh start", func(t *testing.T) {
		sp := sysproxy.NewWindows()
		saveRunner(t, sp)
		// A restart only after 5 min: the supervisor is surely still
		// waiting when it is killed.
		e := newLiveEnvWith(t, 5*time.Minute)
		sup := e.startSupervisor()
		pid := e.waitDaemon(0)
		st := e.connectOverForeign(sp)

		// The daemon first, then its supervisor while it waits out its
		// restart backoff: nothing of ours is left to revert.
		e.killChild(pid, sup)
		sup.kill(t)
		waitLAN(t, sp, sysproxy.Ours(st.Ports.Mixed), 0)

		fresh := e.startSupervisor()
		e.waitDaemon(pid)
		waitLAN(t, sp, foreignPAC, 0)

		if err := fresh.stop(); err != nil {
			t.Fatalf("supervisor exit: %v", err)
		}
		waitLAN(t, sp, foreignPAC, 0)
		requireNoProxyEntries(t, e.dirs)
	})
}

// requireNoProxyEntries asserts the live journal holds no pending system
// proxy entry. Call it only while no daemon runs (R10).
func requireNoProxyEntries(t *testing.T, dirs appdir.Dirs) {
	t.Helper()
	for _, en := range pending(t, filepath.Join(dirs.Journal, safemode.Live.String())) {
		if en.Kind == sysproxy.Kind {
			t.Fatalf("pending system proxy entry after the revert: %s", en.Target)
		}
	}
}

// savedFile is a user file as it was before the test.
type savedFile struct {
	path   string
	data   []byte
	exists bool
}

func saveFile(t *testing.T, path string) savedFile {
	t.Helper()
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		return savedFile{path: path, data: b, exists: true}
	case errors.Is(err, os.ErrNotExist):
		return savedFile{path: path}
	}
	t.Fatalf("read %s: %v", path, err)
	return savedFile{}
}

// same reports whether the file is as saved.
func (f savedFile) same() (bool, error) {
	b, err := os.ReadFile(f.path)
	switch {
	case err == nil:
		return f.exists && bytes.Equal(b, f.data), nil
	case errors.Is(err, os.ErrNotExist):
		return !f.exists, nil
	}
	return false, err
}

func (f savedFile) restore() error {
	if f.exists {
		return os.WriteFile(f.path, f.data, 0o644)
	}
	err := os.Remove(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// userPrompt is what the user's prompt function in the seeded profile
// prints.
const userPrompt = "zatvor-user-prompt>"

// backupSuffix names the one-time copy devtools keeps of a file it edits.
const backupSuffix = ".zatvor.bak"

// TestLiveDevToolsOnRunner: dev tools on the runner's real profile files
// (a disposable runner). The PowerShell profile and .bashrc are first
// seeded with user content (BOM, CRLF, the user's own prompt). Windows
// PowerShell, with the profile dot-sourced, shows the user's prompt and
// sees our proxy; after Disable every file we may edit is byte-equal to
// its copy from before Enable. t.Cleanup gives the runner its own files
// (or their absence) back.
func TestLiveDevToolsOnRunner(t *testing.T) {
	systest.Require(t)
	sp := sysproxy.NewWindows()
	saved := saveRunner(t, sp)

	roots := defaultDevRoots(os.Getenv)
	if roots.Home == "" || roots.Documents == "" || roots.AppData == "" {
		t.Fatalf("user directories unknown: %+v", roots)
	}
	pwshDir := filepath.Join(roots.Documents, "WindowsPowerShell")
	if _, err := os.Stat(pwshDir); errors.Is(err, os.ErrNotExist) {
		// Windows PowerShell's profile directory, as a user who has a
		// profile has it; dev tools never create a tool's directory.
		if err := os.MkdirAll(pwshDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(pwshDir); err != nil {
				t.Errorf("cleanup: remove %s: %v", pwshDir, err)
			}
		})
	}
	profilePath := filepath.Join(pwshDir, "Microsoft.PowerShell_profile.ps1")
	bashrc := filepath.Join(roots.Home, ".bashrc")
	paths := []string{
		profilePath,
		filepath.Join(roots.Documents, "PowerShell", "Microsoft.PowerShell_profile.ps1"),
		bashrc,
		filepath.Join(roots.Home, ".claude", "settings.json"),
		filepath.Join(roots.AppData, "Code", "User", "settings.json"),
	}
	var runner []savedFile // as the runner had them: put back in t.Cleanup
	for _, p := range paths {
		// Enable keeps a one-time copy of an existing file next to it.
		for _, p := range []string{p, p + backupSuffix} {
			f := saveFile(t, p)
			t.Logf("runner's %s: exists %v, %d bytes", f.path, f.exists, len(f.data))
			runner = append(runner, f)
		}
	}
	t.Cleanup(func() {
		for _, f := range runner {
			if ok, err := f.same(); err != nil || !ok {
				if rerr := f.restore(); rerr != nil {
					t.Errorf("cleanup: restore %s: %v", f.path, rerr)
				}
			}
		}
	})
	// The user's own content, which must survive byte for byte: a UTF-8
	// BOM, CRLF lines and a prompt function of theirs in the PowerShell
	// profile (our block wraps that prompt), plain LF lines in .bashrc.
	seeds := map[string]string{
		profilePath: "\ufeff# the user's profile\r\n$env:ZATVOR_SYSTEMTEST_USER = 'kept'\r\nfunction prompt { \"" + userPrompt + "\" }\r\n",
		bashrc:      "# the user's bashrc\nalias ll='ls -l'\n",
	}
	for p, body := range seeds {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var files []savedFile // what Disable must leave
	for _, p := range paths {
		files = append(files, saveFile(t, p))
	}

	e := newLiveEnv(t)
	stop := e.runInProcess()
	st := e.connect(e.addSubscription())
	want := "http://127.0.0.1:" + strconv.Itoa(st.Ports.DevTools)

	var on struct{ Targets []devtools.Target }
	if err := json.Unmarshal(e.mustCall("POST", "/v1/devtools", map[string]bool{"on": true}, http.StatusOK), &on); err != nil {
		t.Fatal(err)
	}
	t.Logf("dev tools on: %+v", on.Targets)
	pwsh5 := false
	for _, tg := range on.Targets {
		if tg.Name == "pwsh5" && tg.Path == profilePath && tg.State == devtools.On {
			pwsh5 = true
		}
	}
	if !pwsh5 {
		t.Fatalf("Windows PowerShell profile %s is not on: %+v", profilePath, on.Targets)
	}
	for _, f := range files {
		if _, seeded := seeds[f.path]; !seeded {
			continue
		}
		if ok, err := f.same(); err != nil || ok {
			t.Fatalf("%s was not edited by Enable (unchanged %v, err %v)", f.path, ok, err)
		}
	}
	stateFile := filepath.Join(e.dirs.Run, StateFileName)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, _ := os.ReadFile(stateFile); string(b) == want {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(stateFile)
			t.Fatalf("state file holds %q, want %q", b, want)
		}
		time.Sleep(50 * time.Millisecond)
	}

	out, err := exec.Command("powershell", "-NoProfile", "-Command", ". $PROFILE; prompt; $env:HTTPS_PROXY").CombinedOutput()
	if err != nil {
		t.Fatalf("powershell: %v\n%s", err, out)
	}
	t.Logf("powershell output:\n%s", out)
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != want {
		t.Fatalf("HTTPS_PROXY in a new PowerShell = %q, want %q", got, want)
	}
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != userPrompt {
		t.Fatalf("the prompt is not the user's own %q (our block must wrap it): %q", userPrompt, lines)
	}

	e.mustCall("POST", "/v1/devtools", map[string]bool{"on": false}, http.StatusOK)
	for _, f := range files {
		if ok, err := f.same(); err != nil || !ok {
			t.Errorf("%s differs from its saved copy after Disable (err %v)", f.path, err)
		}
	}
	for p, body := range seeds {
		if b, err := os.ReadFile(p + backupSuffix); err != nil || string(b) != body {
			t.Errorf("backup %s = %d bytes, %v; want the user's %d bytes", p+backupSuffix, len(b), err, len(body))
		}
	}
	e.mustCall("POST", "/v1/disconnect", nil, http.StatusOK)
	if err := stop(); err != nil {
		t.Fatalf("daemon exit: %v", err)
	}
	requireRunner(t, sp, saved)
}
