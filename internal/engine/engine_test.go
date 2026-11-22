package engine_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/geo/assets"
	"github.com/ArtikGenius/zatvor/internal/geo/merge"
	"github.com/ArtikGenius/zatvor/internal/happ"
	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ports"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

const testLink = "vless://REPLACE_ME_uuid@reality.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.org&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=ab12&spx=%2F&type=tcp#r"

var testPorts = gen.Ports{Mixed: 31001, Probe: 33101, DevTools: 35201, API: 37301}

// ---- fake world: runner, proxy manager, geo, checker, clock ----

type startRec struct {
	dir string
	cfg []byte
}

type world struct {
	mu        sync.Mutex
	log       []string
	starts    []startRec
	runners   []*fakeRunner
	startErrs []error // popped per Start
	applies   []sysapi.ProxyState
	forces    []bool
	applyErrs []error // popped per Apply
	drift     bool
	checks    []engine.CheckResult // popped per check; empty: OK
	onCheck   func(ctx context.Context)
	onApply   func()
	revertErr []error // popped per Revert
	prepares  int
	missing   []merge.MissingTagError // returned by the first Prepare
	bootstrap bool
	fetch     func(ctx context.Context, pg assets.ProfileGeo, dial subfetch.Dialer) error
	testErr   func(cfg []byte) error
	saved     []gen.Ports
	kept      bool // LastRevert reports a connection changed by someone else
}

func (w *world) record(s string) {
	w.mu.Lock()
	w.log = append(w.log, s)
	w.mu.Unlock()
}

func (w *world) logCopy() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.log...)
}

func (w *world) startsCopy() []startRec {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]startRec(nil), w.starts...)
}

func (w *world) runner(i int) *fakeRunner {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.runners[i]
}

func (w *world) count(prefix string) int {
	n := 0
	for _, l := range w.logCopy() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

type fakeRunner struct {
	w   *world
	dir string
	ev  chan xrayrun.Event
}

func (r *fakeRunner) Start(ctx context.Context, cfg []byte, ready xrayrun.ReadyCheck) error {
	r.w.record("start " + r.dir)
	r.w.mu.Lock()
	r.w.starts = append(r.w.starts, startRec{r.dir, append([]byte(nil), cfg...)})
	var err error
	if len(r.w.startErrs) > 0 {
		err, r.w.startErrs = r.w.startErrs[0], r.w.startErrs[1:]
	}
	r.w.mu.Unlock()
	// Like xrayrun: the child was launched (Started); a failed ready check
	// then stops it (Exited without an error).
	r.ev <- xrayrun.Event{Kind: xrayrun.Started}
	if err != nil {
		r.ev <- xrayrun.Event{Kind: xrayrun.Exited}
	}
	return err
}

func (r *fakeRunner) Stop(time.Duration) error { r.w.record("stop " + r.dir); return nil }

func (r *fakeRunner) Events() <-chan xrayrun.Event { return r.ev }

type fakeProxy struct{ w *world }

func (p fakeProxy) Apply(ours sysapi.ProxyState, force bool) error {
	p.w.record(fmt.Sprintf("apply %s force=%v", ours.Server, force))
	p.w.mu.Lock()
	p.w.applies = append(p.w.applies, ours)
	p.w.forces = append(p.w.forces, force)
	var err error
	if len(p.w.applyErrs) > 0 {
		err, p.w.applyErrs = p.w.applyErrs[0], p.w.applyErrs[1:]
	}
	hook := p.w.onApply
	p.w.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (p fakeProxy) Revert() error {
	p.w.record("revert")
	p.w.mu.Lock()
	defer p.w.mu.Unlock()
	var err error
	if len(p.w.revertErr) > 0 {
		err, p.w.revertErr = p.w.revertErr[0], p.w.revertErr[1:]
	}
	return err
}
func (p fakeProxy) LastRevert() []journal.RevertResult {
	p.w.mu.Lock()
	defer p.w.mu.Unlock()
	if !p.w.kept {
		return nil
	}
	return []journal.RevertResult{{Entry: journal.Entry{Kind: sysproxy.Kind, Target: "lan"}, Outcome: journal.SkippedChangedByOther}}
}
func (p fakeProxy) Drifted() (bool, sysapi.ProxyState, error) {
	p.w.mu.Lock()
	defer p.w.mu.Unlock()
	return p.w.drift, sysapi.ProxyState{Enabled: true, Server: "198.51.100.7:8080"}, nil
}

type fakeGeo struct{ w *world }

func (g fakeGeo) Prepare(cfg []byte, pg *assets.ProfileGeo) (string, []merge.MissingTagError, error) {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	g.w.prepares++
	var missing []merge.MissingTagError
	if g.w.prepares == 1 {
		missing = g.w.missing
	}
	return fmt.Sprintf("set%d", g.w.prepares), missing, nil
}

func (g fakeGeo) NeedsBootstrap(cfg []byte, pg assets.ProfileGeo) bool {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	return g.w.bootstrap
}

func (g fakeGeo) Fetch(ctx context.Context, pg assets.ProfileGeo, dial subfetch.Dialer) error {
	g.w.record("fetch")
	if g.w.fetch != nil {
		return g.w.fetch(ctx, pg, dial)
	}
	return nil
}

func (w *world) check(ctx context.Context, dial subfetch.Dialer) engine.CheckResult {
	w.record("check")
	w.mu.Lock()
	hook := w.onCheck
	res := engine.CheckResult{Status: engine.OK, IPv4: "203.0.113.9"}
	if len(w.checks) > 0 {
		res, w.checks = w.checks[0], w.checks[1:]
	}
	w.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	return res
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(2_000_000, 0)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{c.now.Add(d), ch})
	return ch
}

func (c *fakeClock) pending() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.waiters) }

func (c *fakeClock) Advance(d time.Duration) {
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

type rig struct {
	w   *world
	clk *fakeClock
	e   *engine.Engine
}

func newRig(t *testing.T, edit func(*engine.Deps)) *rig {
	t.Helper()
	w := &world{}
	clk := newFakeClock()
	d := engine.Deps{
		XrayPath: "xray-not-used",
		Assets:   fakeGeo{w},
		Proxy:    fakeProxy{w},
		NewRunner: func(dir string) engine.Runner {
			r := &fakeRunner{w: w, dir: dir, ev: make(chan xrayrun.Event, 16)}
			w.mu.Lock()
			w.runners = append(w.runners, r)
			w.mu.Unlock()
			return r
		},
		XrayTest: func(ctx context.Context, cfg []byte, dir string) error {
			w.record("xraytest " + dir)
			if w.testErr != nil {
				return w.testErr(cfg)
			}
			return nil
		},
		Ports:        func(prev gen.Ports) (gen.Ports, bool, error) { return testPorts, prev != testPorts, nil },
		InitialPorts: testPorts,
		SavePorts: func(p gen.Ports) error {
			w.mu.Lock()
			w.saved = append(w.saved, p)
			w.mu.Unlock()
			return nil
		},
		Clock:   clk,
		Checker: w.check,
		Ready:   func(gen.Ports) xrayrun.ReadyCheck { return func(context.Context) error { return nil } },
	}
	if edit != nil {
		edit(&d)
	}
	e := engine.New(d)
	r := &rig{w: w, clk: clk, e: e}
	t.Cleanup(func() { _ = e.Disconnect(context.Background()) })
	return r
}

func linkProfile(t *testing.T) profile.Profile {
	t.Helper()
	n, err := link.Parse(testLink)
	if err != nil {
		t.Fatal(err)
	}
	return profile.Profile{ID: "prof-1", Name: "Test", Node: &n,
		RoutingSource: profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllExceptRU}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitPhase(t *testing.T, e *engine.Engine, p engine.Phase) engine.State {
	t.Helper()
	waitFor(t, "phase "+string(p), func() bool { return e.State().Phase == p })
	return e.State()
}

func hasWarning(s engine.State, code string) bool {
	for _, w := range s.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func errCode(t *testing.T, err error) string {
	t.Helper()
	var ee *engine.Error
	if !errors.As(err, &ee) {
		t.Fatalf("error %v is not *engine.Error", err)
	}
	return ee.Code
}

// cfgInfo is what the tests read back from a generated config.
type cfgInfo struct {
	mixedPort           int
	probeUser, probePas string
	bootstrapRule       bool
}

func parseCfg(t *testing.T, cfg []byte) cfgInfo {
	t.Helper()
	var root struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			Settings struct {
				Accounts []struct{ User, Pass string } `json:"accounts"`
			} `json:"settings"`
		} `json:"inbounds"`
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &root); err != nil {
		t.Fatal(err)
	}
	var ci cfgInfo
	for _, in := range root.Inbounds {
		switch in.Tag {
		case gen.TagMixed:
			ci.mixedPort = in.Port
		case gen.TagProbe:
			if len(in.Settings.Accounts) == 1 {
				ci.probeUser, ci.probePas = in.Settings.Accounts[0].User, in.Settings.Accounts[0].Pass
			}
		}
	}
	// The bootstrap rule is the first rule with only network tcp,udp; the
	// all-except-RU preset ends with one too, so look before the preset's
	// first rule (a domain rule).
	for _, r := range root.Routing.Rules {
		if _, ok := r["domain"]; ok {
			break
		}
		if r["network"] == "tcp,udp" && len(r) == 3 {
			ci.bootstrapRule = true
		}
	}
	return ci
}

// ---- tests ----

func TestProxySetOnlyAfterCheck(t *testing.T) {
	g := newRig(t, nil)
	g.w.onCheck = func(context.Context) {
		if n := g.w.count("apply"); n != 0 {
			t.Errorf("Apply ran %d times before the tunnel check finished", n)
		}
		if n := g.w.count("start"); n != 1 {
			t.Errorf("check ran with %d Xray starts, want 1", n)
		}
	}
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	st := g.e.State()
	if st.Phase != engine.Connected || st.Profile != "prof-1" || st.Check.Status != engine.OK || st.Ports != testPorts {
		t.Fatalf("state = %+v", st)
	}
	want := []string{"xraytest set1", "start set1", "check", "apply 127.0.0.1:31001 force=false"}
	if got := g.w.logCopy(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %q, want %q", got, want)
	}
	if !g.w.applies[0].Enabled || g.w.applies[0].PACURL != "" {
		t.Errorf("applied %+v", g.w.applies[0])
	}
	if d, ok := g.e.ProbeDialer(); !ok || d == nil {
		t.Error("ProbeDialer not available while connected")
	}
}

func TestCheckUnknownMeansNoProxy(t *testing.T) {
	g := newRig(t, nil)
	g.w.checks = []engine.CheckResult{{Status: engine.Unknown}}
	err := g.e.Connect(context.Background(), linkProfile(t))
	if err == nil || errCode(t, err) != "tunnel_check_unknown" {
		t.Fatalf("Connect = %v, want tunnel_check_unknown", err)
	}
	st := g.e.State()
	if st.Phase != engine.Failed || st.Err == nil || st.Err.Code != "tunnel_check_unknown" || st.Check.Status != engine.Unknown {
		t.Fatalf("state = %+v", st)
	}
	if n := g.w.count("apply"); n != 0 {
		t.Fatalf("Apply ran %d times", n)
	}
	if n := g.w.count("stop"); n != 1 {
		t.Fatalf("Xray stopped %d times, want 1", n)
	}
	if _, ok := g.e.ProbeDialer(); ok {
		t.Error("ProbeDialer available after a failed connect")
	}
}

func TestCheckBadMeansNoProxy(t *testing.T) {
	g := newRig(t, nil)
	g.w.checks = []engine.CheckResult{{Status: engine.Bad}}
	if err := g.e.Connect(context.Background(), linkProfile(t)); err == nil || errCode(t, err) != "tunnel_check_bad" {
		t.Fatalf("Connect = %v, want tunnel_check_bad", err)
	}
	if n := g.w.count("apply"); n != 0 {
		t.Fatalf("Apply ran %d times", n)
	}
}

func crash(t *testing.T, g *rig) {
	t.Helper()
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Exited, Err: errors.New("xrayrun: xray exited: exit status 2")}
	st := waitPhase(t, g.e, engine.Degraded)
	if st.Err == nil || st.Err.Code != "xray_exited" {
		t.Fatalf("degraded state = %+v", st)
	}
}

func TestXrayCrashRevertsProxy(t *testing.T) {
	g := newRig(t, nil)
	crash(t, g)
	log := g.w.logCopy()
	if log[len(log)-1] != "revert" {
		t.Fatalf("log = %q, want revert last", log)
	}
	if _, ok := g.e.ProbeDialer(); ok {
		t.Error("ProbeDialer available while Xray is down")
	}
	// A restart that is only scheduled re-applies nothing. Events are
	// handled in order: once the second crash shows, Restarting was handled.
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Restarting, Delay: time.Second}
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Exited, Err: errors.New("second crash")}
	waitFor(t, "second crash", func() bool {
		s := g.e.State()
		return s.Err != nil && strings.Contains(s.Err.Message, "second crash")
	})
	if n := g.w.count("apply"); n != 1 {
		t.Fatalf("Apply ran %d times, want 1", n)
	}
}

func TestRecoveryReapplies(t *testing.T) {
	g := newRig(t, nil)
	crash(t, g)
	r := g.w.runner(0)
	r.ev <- xrayrun.Event{Kind: xrayrun.Restarting, Delay: time.Second}
	r.ev <- xrayrun.Event{Kind: xrayrun.Started}
	st := waitPhase(t, g.e, engine.Connected)
	if st.Err != nil {
		t.Fatalf("state = %+v", st)
	}
	want := []string{"check", "apply 127.0.0.1:31001 force=false", "revert", "check", "apply 127.0.0.1:31001 force=false"}
	log := g.w.logCopy()
	if got := log[2:]; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("log = %q, want ... %q", log, want)
	}
}

func TestRecoveryCheckFailsKeepsProxyOff(t *testing.T) {
	g := newRig(t, nil)
	crash(t, g)
	g.w.mu.Lock()
	g.w.checks = []engine.CheckResult{{Status: engine.Unknown}, {Status: engine.OK, IPv4: "203.0.113.9"}}
	g.w.mu.Unlock()
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Started}
	waitFor(t, "recovery check", func() bool { return g.w.count("check") == 2 })
	waitFor(t, "recheck timer", func() bool { return g.clk.pending() >= 2 }) // drift + recheck
	if st := g.e.State(); st.Phase != engine.Degraded || st.Check.Status != engine.Unknown {
		t.Fatalf("state = %+v", st)
	}
	if n := g.w.count("apply"); n != 1 {
		t.Fatalf("Apply ran %d times after an unknown check", n)
	}
	if _, ok := g.e.ProbeDialer(); !ok {
		t.Error("ProbeDialer unavailable while Degraded with Xray running")
	}
	g.clk.Advance(10 * time.Second)
	waitPhase(t, g.e, engine.Connected)
	if n := g.w.count("apply"); n != 2 {
		t.Fatalf("Apply ran %d times, want 2", n)
	}
}

func TestRecoveryConflictDoesNotForce(t *testing.T) {
	g := newRig(t, nil)
	crash(t, g)
	g.w.mu.Lock()
	g.w.applyErrs = []error{&sysproxy.ConflictError{Conn: "Office VPN", Current: sysapi.ProxyState{PACURL: "http://198.51.100.1/p.pac"}}}
	g.w.mu.Unlock()
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Started}
	waitFor(t, "conflict warning", func() bool { return hasWarning(g.e.State(), "proxy_conflict") })
	st := g.e.State()
	if st.Phase != engine.Degraded {
		t.Fatalf("state = %+v", st)
	}
	for _, w := range st.Warnings {
		if w.Code == "proxy_conflict" && (w.Conflict == nil || w.Conflict.Conn != "Office VPN" || !w.Conflict.PAC || strings.Contains(w.Message, "198.51.100.1")) {
			t.Fatalf("conflict warning = %+v", w)
		}
	}
	for _, f := range g.w.forces {
		if f {
			t.Fatal("recovery forced the proxy")
		}
	}
}

func TestGaveUpStopsRunner(t *testing.T) {
	g := newRig(t, nil)
	crash(t, g)
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.GaveUp, Err: errors.New("xrayrun: too many crashes")}
	waitFor(t, "gave up", func() bool {
		s := g.e.State()
		return s.Err != nil && s.Err.Code == "xray_gave_up"
	})
	waitFor(t, "stop after GaveUp", func() bool { return g.w.count("stop") == 1 })
	if st := g.e.State(); st.Phase != engine.Degraded {
		t.Fatalf("state = %+v", st)
	}
}

func TestPortConflictRepicks(t *testing.T) {
	busy := 31001
	var mu sync.Mutex
	g := newRig(t, func(d *engine.Deps) {
		rnd := rand.New(rand.NewSource(1))
		d.Ports = func(prev gen.Ports) (gen.Ports, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return ports.Pick(prev, func(p int) error {
				if p == busy {
					return errors.New("in use")
				}
				return nil
			}, rnd)
		}
	})
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	st := g.e.State()
	if st.Ports.Mixed == busy || st.Ports.Mixed == 0 {
		t.Fatalf("Mixed port = %d, want a re-picked one", st.Ports.Mixed)
	}
	if st.Ports.Probe != testPorts.Probe || st.Ports.API != testPorts.API {
		t.Errorf("free ports changed: %+v", st.Ports)
	}
	ci := parseCfg(t, g.w.startsCopy()[0].cfg)
	if ci.mixedPort != st.Ports.Mixed {
		t.Errorf("config mixed port = %d, want %d", ci.mixedPort, st.Ports.Mixed)
	}
	if want := "127.0.0.1:" + strconv.Itoa(st.Ports.Mixed); g.w.applies[0].Server != want {
		t.Errorf("proxy server = %q, want %q", g.w.applies[0].Server, want)
	}
	if len(g.w.saved) != 1 || g.w.saved[0] != st.Ports {
		t.Errorf("saved ports = %+v, want %+v", g.w.saved, st.Ports)
	}
}

func TestDriftWarnsNoOverwrite(t *testing.T) {
	g := newRig(t, nil)
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "drift timer", func() bool { return g.clk.pending() >= 1 })
	g.w.mu.Lock()
	g.w.drift = true
	g.w.mu.Unlock()
	g.clk.Advance(5 * time.Second)
	waitFor(t, "drift warning", func() bool { return hasWarning(g.e.State(), "proxy_drift") })
	if n := g.w.count("apply"); n != 1 {
		t.Fatalf("Apply ran %d times, want 1 (no overwrite)", n)
	}
	if n := g.w.count("revert"); n != 0 {
		t.Fatalf("Revert ran %d times", n)
	}
	st := g.e.State()
	if st.Phase != engine.Connected {
		t.Fatalf("state = %+v", st)
	}
	for _, w := range st.Warnings {
		if w.Code == "proxy_drift" && strings.Contains(w.Message, "198.51.100.7") {
			t.Fatalf("drift warning leaks the foreign value: %q", w.Message)
		}
	}
	// The event stream carries the warning too.
	var last engine.State
	for len(g.e.Events()) > 0 {
		last = <-g.e.Events()
	}
	if !hasWarning(last, "proxy_drift") {
		t.Errorf("latest event = %+v, want the drift warning", last)
	}
}

func TestEventsNeverBlock(t *testing.T) {
	g := newRig(t, nil)
	// Nobody reads Events: many transitions must not block the engine.
	for i := 0; i < 40; i++ {
		if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
			t.Fatal(err)
		}
		if err := g.e.Disconnect(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var last engine.State
	n := 0
	for len(g.e.Events()) > 0 {
		last = <-g.e.Events()
		n++
	}
	if n == 0 || last.Phase != engine.Disconnected {
		t.Fatalf("drained %d events, last %+v", n, last)
	}
}

func TestDisconnectOrder(t *testing.T) {
	g := newRig(t, nil)
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	if err := g.e.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	log := g.w.logCopy()
	if got := log[len(log)-2:]; got[0] != "revert" || got[1] != "stop set1" {
		t.Fatalf("log = %q, want revert then stop", log)
	}
	if st := g.e.State(); st.Phase != engine.Disconnected || st.Err != nil {
		t.Fatalf("state = %+v", st)
	}
	if _, ok := g.e.ProbeDialer(); ok {
		t.Error("ProbeDialer available after Disconnect")
	}
}

func TestDisconnectKeepsForeignWarns(t *testing.T) {
	g := newRig(t, nil)
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	g.w.mu.Lock()
	g.w.kept = true
	g.w.mu.Unlock()
	if err := g.e.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := g.e.State(); st.Phase != engine.Disconnected || !hasWarning(st, "proxy_kept_foreign") {
		t.Fatalf("state = %+v", st)
	}
}

func TestMissingGeoTagFailsBeforeStart(t *testing.T) {
	g := newRig(t, nil)
	g.w.missing = []merge.MissingTagError{{Tag: "category-xyz", Kind: "geosite", Searched: []string{"roscomvpn", "v2fly"}}}
	err := g.e.Connect(context.Background(), linkProfile(t))
	if err == nil || errCode(t, err) != "geo_missing_tag" {
		t.Fatalf("Connect = %v, want geo_missing_tag", err)
	}
	var ee *engine.Error
	errors.As(err, &ee)
	if !strings.Contains(ee.Message, `geosite tag "category-xyz" is missing from roscomvpn and v2fly`) {
		t.Errorf("message = %q", ee.Message)
	}
	if n := g.w.count("start") + g.w.count("xraytest"); n != 0 {
		t.Fatalf("Xray ran %d times", n)
	}
}

func TestXrayTestFailureStopsBeforeStart(t *testing.T) {
	g := newRig(t, nil)
	g.w.testErr = func([]byte) error { return errors.New("xray run -test failed (exit status 23): bad") }
	if err := g.e.Connect(context.Background(), linkProfile(t)); err == nil || errCode(t, err) != "xray_test_failed" {
		t.Fatalf("Connect = %v", err)
	}
	if n := g.w.count("start"); n != 0 {
		t.Fatalf("Xray started %d times", n)
	}
}

func TestProxyConflictResolve(t *testing.T) {
	for _, force := range []bool{true, false} {
		t.Run(fmt.Sprint("force=", force), func(t *testing.T) {
			g := newRig(t, nil)
			g.w.applyErrs = []error{&sysproxy.ConflictError{Conn: "", Current: sysapi.ProxyState{Enabled: true, Server: "198.51.100.7:8080"}}}
			err := g.e.Connect(context.Background(), linkProfile(t))
			if err == nil || errCode(t, err) != "proxy_conflict" {
				t.Fatalf("Connect = %v, want proxy_conflict", err)
			}
			st := g.e.State()
			if st.Phase != engine.Failed || st.Err.Conflict == nil || st.Err.Conflict.Conn != "" || st.Err.Conflict.PAC {
				t.Fatalf("state = %+v", st)
			}
			if strings.Contains(st.Err.Message, "198.51.100.7") {
				t.Fatalf("message leaks the foreign proxy: %q", st.Err.Message)
			}
			if n := g.w.count("stop"); n != 1 {
				t.Fatalf("Xray stopped %d times, want 1", n)
			}
			if err := g.e.ResolveConflict(context.Background(), force); err != nil {
				t.Fatal(err)
			}
			st = g.e.State()
			if force {
				if st.Phase != engine.Connected || len(g.w.forces) != 2 || !g.w.forces[1] {
					t.Fatalf("state = %+v forces = %v", st, g.w.forces)
				}
			} else if st.Phase != engine.Disconnected || len(g.w.forces) != 1 {
				t.Fatalf("state = %+v forces = %v", st, g.w.forces)
			}
			if err := g.e.ResolveConflict(context.Background(), true); err == nil || errCode(t, err) != "no_conflict" {
				t.Fatalf("second ResolveConflict = %v, want no_conflict", err)
			}
		})
	}
}

// socksRecorder is a minimal SOCKS5 server that records the
// username/password a client offers and answers CONNECT with success.
type socksRecorder struct {
	ln   net.Listener
	mu   sync.Mutex
	auth []string
}

// newSocksRecorder listens on a free loopback port.
func newSocksRecorder(t *testing.T) *socksRecorder {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &socksRecorder{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *socksRecorder) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *socksRecorder) serve(c net.Conn) {
	defer c.Close()
	b := make([]byte, 2)
	if _, err := io.ReadFull(c, b); err != nil {
		return
	}
	methods := make([]byte, b[1])
	io.ReadFull(c, methods)
	c.Write([]byte{5, 2}) // username/password
	if _, err := io.ReadFull(c, b); err != nil {
		return
	}
	user := make([]byte, b[1])
	io.ReadFull(c, user)
	l := make([]byte, 1)
	io.ReadFull(c, l)
	pass := make([]byte, l[0])
	io.ReadFull(c, pass)
	s.mu.Lock()
	s.auth = append(s.auth, string(user)+":"+string(pass))
	s.mu.Unlock()
	c.Write([]byte{1, 0})
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	switch hdr[3] {
	case 1:
		io.ReadFull(c, make([]byte, 4))
	case 3:
		io.ReadFull(c, l)
		io.ReadFull(c, make([]byte, l[0]))
	case 4:
		io.ReadFull(c, make([]byte, 16))
	}
	io.ReadFull(c, make([]byte, 2))
	rep := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(rep[8:], 9)
	c.Write(rep)
}

func happProfile(t *testing.T) profile.Profile {
	p := linkProfile(t)
	p.RoutingSource = profile.Source{Kind: profile.SourceHapp, Happ: &happ.Profile{
		Geositeurl: "https://geo.example/geosite.dat", Geoipurl: "https://geo.example/geoip.dat",
		DirectSites: []string{"geosite:category-xyz"},
	}}
	return p
}

func TestBootstrapThenSwitch(t *testing.T) {
	socks := newSocksRecorder(t)
	pp := testPorts
	pp.Probe = socks.port()
	g := newRig(t, func(d *engine.Deps) {
		d.InitialPorts = pp
		d.Ports = func(prev gen.Ports) (gen.Ports, bool, error) { return pp, prev != pp, nil }
	})
	g.w.bootstrap = true
	fetched := make(chan assets.ProfileGeo, 1)
	g.w.fetch = func(ctx context.Context, pg assets.ProfileGeo, dial subfetch.Dialer) error {
		c, err := dial(ctx, "tcp", "geo.example:443")
		if err != nil {
			return err
		}
		c.Close()
		fetched <- pg
		return nil
	}
	if err := g.e.Connect(context.Background(), happProfile(t)); err != nil {
		t.Fatal(err)
	}
	pg := <-fetched
	if pg.GeoSiteURL != "https://geo.example/geosite.dat" || pg.GeoIPURL != "https://geo.example/geoip.dat" {
		t.Errorf("Fetch got %+v", pg)
	}
	waitFor(t, "switch", func() bool { return len(g.w.startsCopy()) == 2 && !g.e.State().GeoBootstrap })
	st := waitPhase(t, g.e, engine.Connected)
	starts := g.w.startsCopy()
	first, second := parseCfg(t, starts[0].cfg), parseCfg(t, starts[1].cfg)
	if !first.bootstrapRule {
		t.Error("first config has no bootstrap rule")
	}
	if second.bootstrapRule {
		t.Error("second config still has the bootstrap rule")
	}
	if !strings.Contains(string(starts[1].cfg), "geosite:category-xyz") {
		t.Error("second config lacks the profile's own rules")
	}
	if starts[0].dir == starts[1].dir {
		t.Errorf("both runs use asset dir %q", starts[0].dir)
	}
	socks.mu.Lock()
	auth := append([]string(nil), socks.auth...)
	socks.mu.Unlock()
	if len(auth) != 1 || auth[0] != first.probeUser+":"+first.probePas || first.probeUser == "" {
		t.Fatalf("Fetch dialed with %q, want the probe credentials", auth)
	}
	if second.probeUser != first.probeUser || second.mixedPort != first.mixedPort {
		t.Error("switch changed the probe credentials or ports")
	}
	if n := g.w.count("revert"); n != 0 {
		t.Fatalf("the switch reverted the proxy %d times", n)
	}
	if n := g.w.count("apply"); n != 1 {
		t.Fatalf("Apply ran %d times, want 1", n)
	}
	if len(st.Warnings) != 0 {
		t.Errorf("warnings = %+v", st.Warnings)
	}
}

func TestGeoSwitchRollback(t *testing.T) {
	g := newRig(t, nil)
	g.w.bootstrap = true
	g.w.startErrs = []error{nil, errors.New("xrayrun: xray not ready")}
	if err := g.e.Connect(context.Background(), happProfile(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "rollback", func() bool { return hasWarning(g.e.State(), "geo_switch_failed") })
	starts := g.w.startsCopy()
	if len(starts) != 3 {
		t.Fatalf("%d starts, want 3 (bootstrap, new, rollback)", len(starts))
	}
	if starts[2].dir != starts[0].dir || string(starts[2].cfg) != string(starts[0].cfg) {
		t.Errorf("rollback ran dir %q, want the bootstrap set %q and config", starts[2].dir, starts[0].dir)
	}
	st := g.e.State()
	if st.Phase != engine.Connected || !st.GeoBootstrap {
		t.Fatalf("state = %+v", st)
	}
	if n := g.w.count("revert"); n != 0 {
		t.Fatalf("rollback reverted the proxy %d times", n)
	}
	if _, ok := g.e.ProbeDialer(); !ok {
		t.Error("ProbeDialer gone after rollback")
	}
}

// ---- TunnelCheck ----

func ipServer(t *testing.T, body string, status int) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func directDial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func TestTunnelCheckAgreement(t *testing.T) {
	a := ipServer(t, "203.0.113.9\n", 200)
	a2 := ipServer(t, " 203.0.113.9 ", 200)
	b := ipServer(t, "198.51.100.4", 200)
	v6 := ipServer(t, "2001:db8::5\n", 200)
	junk := ipServer(t, "<html>captive portal</html>", 200)
	fail := ipServer(t, "203.0.113.9", 503)
	closed := "http://127.0.0.1:1"

	cases := []struct {
		name     string
		services []string
		want     engine.Status
		ip       string
	}{
		{"two agree", []string{a, a2}, engine.OK, "203.0.113.9"},
		{"two agree one differs", []string{a, b, a2}, engine.OK, "203.0.113.9"},
		{"one answers", []string{a, fail}, engine.Unknown, ""},
		{"one answers, junk, closed", []string{a, junk, closed}, engine.Unknown, ""},
		{"two disagree", []string{a, b}, engine.Bad, ""},
		{"v4 and v6 only", []string{a, v6}, engine.Unknown, ""},
		{"none", nil, engine.Unknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := engine.TunnelCheck(context.Background(), directDial, tc.services)
			if got.Status != tc.want || got.IPv4 != tc.ip {
				t.Fatalf("got %+v, want %s %q", got, tc.want, tc.ip)
			}
			if got.At.IsZero() {
				t.Error("At not set")
			}
		})
	}
}

func TestDefaultCheckServices(t *testing.T) {
	want := []string{"https://api.ipify.org", "https://ifconfig.co/ip", "https://icanhazip.com"}
	if strings.Join(engine.DefaultCheckServices, ",") != strings.Join(want, ",") {
		t.Fatalf("DefaultCheckServices = %v", engine.DefaultCheckServices)
	}
	for _, s := range engine.DefaultCheckServices {
		if strings.Contains(s, ".ru/") || strings.HasSuffix(s, ".ru") || !strings.HasPrefix(s, "https://") {
			t.Errorf("service %q breaks R3.10", s)
		}
	}
}

// TestGeoSwitchRollbackFails: the new set and the rollback both fail to
// start. The failed runners' queued events (Started, then Exited from their
// own stop) must not count as a recovery: Degraded, proxy reverted, no
// probe dialer, no further check or Apply.
func TestGeoSwitchRollbackFails(t *testing.T) {
	g := newRig(t, nil)
	g.w.bootstrap = true
	g.w.startErrs = []error{nil, errors.New("xrayrun: xray not ready"), errors.New("xrayrun: xray not ready")}
	if err := g.e.Connect(context.Background(), happProfile(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "degraded", func() bool {
		s := g.e.State()
		return s.Phase == engine.Degraded && s.Err != nil && s.Err.Code == "xray_start_failed"
	})
	checks := g.w.count("check")
	// Let the loop go round several times; stale events would be read.
	for i := 0; i < 4; i++ {
		waitFor(t, "drift timer", func() bool { return g.clk.pending() >= 1 })
		g.clk.Advance(5 * time.Second)
		waitFor(t, "loop re-armed", func() bool { return g.clk.pending() >= 1 })
	}
	if n := g.w.count("check"); n != checks {
		t.Fatalf("%d checks after the failed rollback, want %d", n, checks)
	}
	if n := g.w.count("apply"); n != 1 {
		t.Fatalf("Apply ran %d times", n)
	}
	if n := g.w.count("revert"); n != 1 {
		t.Fatalf("Revert ran %d times, want 1", n)
	}
	if _, ok := g.e.ProbeDialer(); ok {
		t.Fatal("ProbeDialer available with no Xray running")
	}
	if st := g.e.State(); st.Phase != engine.Degraded {
		t.Fatalf("state = %+v", st)
	}
}

// TestDisconnectCancelsInflightConnect: Disconnect arrives while Connect
// applies the proxy. Connect returns canceled and undoes itself, revert
// before stop; the engine ends Disconnected with the proxy reverted.
func TestDisconnectCancelsInflightConnect(t *testing.T) {
	g := newRig(t, nil)
	var connCtx context.Context
	g.w.onCheck = func(ctx context.Context) { connCtx = ctx }
	discDone := make(chan error, 1)
	g.w.onApply = func() {
		go func() { discDone <- g.e.Disconnect(context.Background()) }()
		<-connCtx.Done() // Disconnect has cancelled this connect
	}
	err := g.e.Connect(context.Background(), linkProfile(t))
	if err == nil || errCode(t, err) != "canceled" {
		t.Fatalf("Connect = %v, want canceled", err)
	}
	if err := <-discDone; err != nil {
		t.Fatal(err)
	}
	log := g.w.logCopy()
	want := []string{"apply 127.0.0.1:31001 force=false", "revert", "stop set1"}
	if got := log[len(log)-3:]; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("log = %q, want it to end with %q", log, want)
	}
	if st := g.e.State(); st.Phase != engine.Disconnected {
		t.Fatalf("state = %+v", st)
	}
	if _, ok := g.e.ProbeDialer(); ok {
		t.Fatal("ProbeDialer available after Disconnect")
	}
}

// TestSupersededConnect: a second Connect cancels the first one in flight;
// the first returns superseded, the second connects.
func TestSupersededConnect(t *testing.T) {
	g := newRig(t, nil)
	inCheck := make(chan struct{})
	var once sync.Once
	g.w.onCheck = func(ctx context.Context) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(inCheck)
			<-ctx.Done()
		}
	}
	firstErr := make(chan error, 1)
	go func() { firstErr <- g.e.Connect(context.Background(), linkProfile(t)) }()
	<-inCheck
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	if err := <-firstErr; err == nil || errCode(t, err) != "superseded" {
		t.Fatalf("first Connect = %v, want superseded", err)
	}
	if st := g.e.State(); st.Phase != engine.Connected {
		t.Fatalf("state = %+v", st)
	}
	if n := g.w.count("apply"); n != 1 {
		t.Fatalf("Apply ran %d times, want 1", n)
	}
}

// TestRevertRetriedUntilSuccess: a revert that fails after a crash is
// retried on the re-check timer until it succeeds.
func TestRevertRetriedUntilSuccess(t *testing.T) {
	g := newRig(t, nil)
	g.w.revertErr = []error{errors.New("registry busy"), errors.New("registry busy")}
	crash(t, g)
	if !hasWarning(g.e.State(), "proxy_revert_failed") {
		t.Fatalf("state = %+v", g.e.State())
	}
	for want := 2; want <= 3; want++ {
		waitFor(t, "recheck timer", func() bool { return g.clk.pending() >= 2 }) // drift + recheck
		g.clk.Advance(10 * time.Second)
		waitFor(t, "revert retry", func() bool { return g.w.count("revert") == want })
	}
	waitFor(t, "warning cleared", func() bool { return !hasWarning(g.e.State(), "proxy_revert_failed") })
	if st := g.e.State(); st.Phase != engine.Degraded {
		t.Fatalf("state = %+v", st)
	}
}

// TestGeoFetchErrorHidesURL: a failed download warns without quoting the
// profile's geo URL.
func TestGeoFetchErrorHidesURL(t *testing.T) {
	g := newRig(t, nil)
	g.w.bootstrap = true
	g.w.fetch = func(ctx context.Context, pg assets.ProfileGeo, dial subfetch.Dialer) error {
		return fmt.Errorf("geo assets: %w", &url.Error{Op: "Get", URL: pg.GeoSiteURL + "?token=abcdef123", Err: errors.New("connection refused")})
	}
	if err := g.e.Connect(context.Background(), happProfile(t)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fetch warning", func() bool { return hasWarning(g.e.State(), "geo_fetch_failed") })
	for _, w := range g.e.State().Warnings {
		if strings.Contains(w.Message, "geo.example") || strings.Contains(w.Message, "abcdef123") {
			t.Fatalf("warning quotes the URL: %q", w.Message)
		}
	}
}

// TestCancelledConnectKeepsLiveSession: a Connect whose ctx is already
// cancelled returns an error and leaves the live session alone; its loop
// still handles a crash (proxy reverted).
func TestCancelledConnectKeepsLiveSession(t *testing.T) {
	g := newRig(t, nil)
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.e.Connect(ctx, linkProfile(t)); err == nil || errCode(t, err) != "canceled" {
		t.Fatalf("Connect = %v, want canceled", err)
	}
	if st := g.e.State(); st.Phase != engine.Connected || st.Err != nil {
		t.Fatalf("state = %+v, want the live session's Connected", st)
	}
	if _, ok := g.e.ProbeDialer(); !ok {
		t.Fatal("ProbeDialer gone")
	}
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Exited, Err: errors.New("crash")}
	waitPhase(t, g.e, engine.Degraded)
	if log := g.w.logCopy(); log[len(log)-1] != "revert" {
		t.Fatalf("log = %q, want the crash revert", log)
	}
}

// TestSupersededWhileWaiting: A holds opMu; B registers (superseding A)
// and is held before opMu; C supersedes B and connects; only then does B
// take opMu. B returns superseded and leaves C's Connected state alone.
func TestSupersededWhileWaiting(t *testing.T) {
	g := newRig(t, nil)
	release, cDone := make(chan struct{}), make(chan struct{})
	engine.SetBeforeOpLock(g.e, func(seq uint64) {
		if seq == 2 {
			<-cDone
		}
	})
	var once sync.Once
	g.w.onCheck = func(ctx context.Context) {
		first := false
		once.Do(func() { first = true })
		if first {
			<-release // A keeps opMu until B and C are registered
		}
	}
	errs := make([]chan error, 3)
	for i := range errs {
		errs[i] = make(chan error, 1)
	}
	go func() { errs[0] <- g.e.Connect(context.Background(), linkProfile(t)) }()
	waitFor(t, "A in check", func() bool { return g.w.count("check") == 1 })
	go func() { errs[1] <- g.e.Connect(context.Background(), linkProfile(t)) }()
	waitFor(t, "B registered", func() bool { return engine.ConnSeq(g.e) == 2 })
	go func() { errs[2] <- g.e.Connect(context.Background(), linkProfile(t)) }()
	waitFor(t, "C registered", func() bool { return engine.ConnSeq(g.e) == 3 })
	close(release)
	if err := <-errs[0]; err == nil || errCode(t, err) != "superseded" {
		t.Fatalf("A = %v, want superseded", err)
	}
	if err := <-errs[2]; err != nil {
		t.Fatalf("C = %v", err)
	}
	close(cDone)
	if err := <-errs[1]; err == nil || errCode(t, err) != "superseded" {
		t.Fatalf("B = %v, want superseded", err)
	}
	st := g.e.State()
	if st.Phase != engine.Connected || st.Err != nil {
		t.Fatalf("final state = %+v, want C's Connected", st)
	}
	if _, ok := g.e.ProbeDialer(); !ok {
		t.Fatal("ProbeDialer gone after C connected")
	}
	if n := g.w.count("apply"); n != 1 {
		t.Fatalf("Apply ran %d times, want 1 (C)", n)
	}
}

// TestInterruptedRecoveryRetries: a Connect interrupts a recovery check
// and is then cancelled before it takes opMu. The recovery changes nothing,
// the session keeps its loop, and the re-check timer finishes it.
func TestInterruptedRecoveryRetries(t *testing.T) {
	g := newRig(t, nil)
	crash(t, g)
	inCheck := make(chan struct{})
	g.w.onCheck = func(ctx context.Context) {
		close(inCheck)
		<-ctx.Done() // interrupted by the Connect below
		g.w.mu.Lock()
		g.w.onCheck = nil
		g.w.mu.Unlock()
	}
	bctx, cancel := context.WithCancel(context.Background())
	engine.SetBeforeOpLock(g.e, func(seq uint64) {
		if seq == 2 {
			cancel() // B gives up before it takes opMu
		}
	})
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Started}
	<-inCheck
	if err := g.e.Connect(bctx, linkProfile(t)); err == nil || errCode(t, err) != "canceled" {
		t.Fatalf("B = %v, want canceled", err)
	}
	if st := g.e.State(); st.Phase != engine.Degraded {
		t.Fatalf("state = %+v", st)
	}
	waitFor(t, "recheck timer", func() bool { return g.clk.pending() >= 2 })
	g.clk.Advance(10 * time.Second)
	waitPhase(t, g.e, engine.Connected)
	if n := g.w.count("apply"); n != 2 {
		t.Fatalf("Apply ran %d times, want 2", n)
	}
}

// TestPreCancelledConnectDoesNotDetach: while A is in flight, a Connect
// with a dead ctx returns canceled without registering, so a Disconnect
// still cancels A promptly.
func TestPreCancelledConnectDoesNotDetach(t *testing.T) {
	g := newRig(t, nil)
	inCheck, giveUp := make(chan struct{}), make(chan struct{})
	g.w.onCheck = func(ctx context.Context) {
		close(inCheck)
		select { // only a cancel of A's own ctx ends this (giveUp: test failed)
		case <-ctx.Done():
		case <-giveUp:
		}
	}
	aErr := make(chan error, 1)
	go func() { aErr <- g.e.Connect(context.Background(), linkProfile(t)) }()
	<-inCheck
	defer func() {
		select {
		case <-giveUp:
		default:
			close(giveUp) // never leave A blocked
		}
	}()
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	bErr := make(chan error, 1)
	go func() { bErr <- g.e.Connect(dead, linkProfile(t)) }()
	select {
	case err := <-bErr:
		if err == nil || errCode(t, err) != "canceled" {
			t.Fatalf("B = %v, want canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pre-cancelled Connect blocked behind the connect in flight")
	}
	if engine.ConnSeq(g.e) != 1 {
		t.Fatalf("B registered (ConnSeq %d)", engine.ConnSeq(g.e))
	}
	discDone := make(chan error, 1)
	go func() { discDone <- g.e.Disconnect(context.Background()) }()
	select {
	case err := <-aErr:
		if err == nil || errCode(t, err) != "canceled" {
			t.Fatalf("A = %v, want canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Disconnect did not cancel the connect in flight")
	}
	if err := <-discDone; err != nil {
		t.Fatal(err)
	}
	if st := g.e.State(); st.Phase != engine.Disconnected {
		t.Fatalf("state = %+v", st)
	}
	if n := g.w.count("apply"); n != 0 {
		t.Fatalf("Apply ran %d times", n)
	}
}

// TestInterruptedWorkStaysCancelledWhileConnectWaits: B interrupted a
// recovery check and waits before opMu. A re-check that wins opMu
// meanwhile must not start a new check; it lets B in.
func TestInterruptedWorkStaysCancelledWhileConnectWaits(t *testing.T) {
	g := newRig(t, nil)
	crash(t, g)
	inCheck := make(chan struct{})
	g.w.onCheck = func(ctx context.Context) {
		close(inCheck)
		<-ctx.Done()
		g.w.mu.Lock()
		g.w.onCheck = nil
		g.w.mu.Unlock()
	}
	bWaiting, releaseB := make(chan struct{}), make(chan struct{})
	engine.SetBeforeOpLock(g.e, func(seq uint64) {
		if seq == 2 {
			close(bWaiting)
			<-releaseB
		}
	})
	g.w.runner(0).ev <- xrayrun.Event{Kind: xrayrun.Started}
	<-inCheck
	bErr := make(chan error, 1)
	go func() { bErr <- g.e.Connect(context.Background(), linkProfile(t)) }()
	<-bWaiting
	// The interrupted recovery arms the re-check; fire it while B waits.
	waitFor(t, "recheck timer", func() bool { return g.clk.pending() >= 2 })
	checks := g.w.count("check")
	g.clk.Advance(10 * time.Second)
	waitFor(t, "recheck handled", func() bool { return g.clk.pending() >= 2 })
	if n := g.w.count("check"); n != checks {
		t.Fatalf("re-check ran %d new checks while a connect waited", n-checks)
	}
	close(releaseB)
	if err := <-bErr; err != nil {
		t.Fatal(err)
	}
	if st := g.e.State(); st.Phase != engine.Connected {
		t.Fatalf("state = %+v", st)
	}
}

func TestRecheckNotConnected(t *testing.T) {
	g := newRig(t, nil)
	if _, err := g.e.Recheck(context.Background()); err == nil || errCode(t, err) != engine.CodeNotConnected {
		t.Fatalf("Recheck while disconnected = %v, want not_connected", err)
	}
	crash(t, g)
	checks := g.w.count("check")
	if _, err := g.e.Recheck(context.Background()); err == nil || errCode(t, err) != engine.CodeNotConnected {
		t.Fatalf("Recheck while degraded = %v, want not_connected", err)
	}
	if n := g.w.count("check"); n != checks {
		t.Errorf("Recheck ran %d checks while not connected", n-checks)
	}
}

func TestRecheckStoresResult(t *testing.T) {
	g := newRig(t, nil)
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	for len(g.e.Events()) > 0 {
		<-g.e.Events()
	}
	g.clk.Advance(time.Minute)
	g.w.mu.Lock()
	g.w.checks = []engine.CheckResult{{Status: engine.Bad}, {Status: engine.OK, IPv4: "203.0.113.10"}}
	g.w.mu.Unlock()

	res, err := g.e.Recheck(context.Background())
	if err != nil || res.Status != engine.Bad || !res.At.Equal(g.clk.Now()) {
		t.Fatalf("Recheck = %+v, %v; want bad at %v", res, err, g.clk.Now())
	}
	st := g.e.State()
	if st.Phase != engine.Connected || st.Check != res {
		t.Fatalf("state = %+v, want connected with check %+v", st, res)
	}
	select {
	case ev := <-g.e.Events():
		if ev.Check != res {
			t.Errorf("event check = %+v, want %+v", ev.Check, res)
		}
	default:
		t.Error("Recheck published no event")
	}
	if res, err := g.e.Recheck(context.Background()); err != nil || res.Status != engine.OK || res.IPv4 != "203.0.113.10" {
		t.Fatalf("second Recheck = %+v, %v", res, err)
	}
	if n := g.w.count("apply"); n != 1 {
		t.Errorf("Recheck touched the system proxy: %d applies", n)
	}
}

// A Connect or Disconnect that arrives while Recheck runs interrupts it,
// and gets the engine right after.
func TestRecheckYieldsToOperations(t *testing.T) {
	for _, op := range []string{"connect", "disconnect"} {
		t.Run(op, func(t *testing.T) {
			g := newRig(t, nil)
			if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
				t.Fatal(err)
			}
			inCheck := make(chan struct{})
			g.w.mu.Lock()
			g.w.onCheck = func(ctx context.Context) {
				g.w.mu.Lock()
				g.w.onCheck = nil
				g.w.mu.Unlock()
				close(inCheck)
				<-ctx.Done()
			}
			g.w.mu.Unlock()
			type result struct {
				res engine.CheckResult
				err error
			}
			done := make(chan result, 1)
			go func() {
				res, err := g.e.Recheck(context.Background())
				done <- result{res, err}
			}()
			<-inCheck
			var err error
			want := engine.Connected
			if op == "connect" {
				err = g.e.Connect(context.Background(), linkProfile(t))
			} else {
				err, want = g.e.Disconnect(context.Background()), engine.Disconnected
			}
			if err != nil {
				t.Fatalf("%s: %v", op, err)
			}
			r := <-done
			if r.err == nil || errCode(t, r.err) != engine.CodeCanceled {
				t.Fatalf("Recheck = %+v, %v; want canceled", r.res, r.err)
			}
			if st := g.e.State(); st.Phase != want {
				t.Fatalf("state = %+v, want %s", st, want)
			}
		})
	}
}

type outRunner struct {
	engine.Runner
	out string
}

func (r outRunner) Output() string { return r.out }

func TestXrayOutputOfLastRun(t *testing.T) {
	g := newRig(t, func(d *engine.Deps) {
		prev := d.NewRunner
		d.NewRunner = func(dir string) engine.Runner { return outRunner{prev(dir), "xray said " + dir} }
	})
	if out := g.e.XrayOutput(); out != "" {
		t.Fatalf("XrayOutput before any run = %q", out)
	}
	if err := g.e.Connect(context.Background(), linkProfile(t)); err != nil {
		t.Fatal(err)
	}
	if err := g.e.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out := g.e.XrayOutput(); out != "xray said set1" {
		t.Errorf("XrayOutput after the run = %q", out)
	}
	if out := newRig(t, nil).e.XrayOutput(); out != "" {
		t.Errorf("XrayOutput of a runner without output = %q", out)
	}
}
