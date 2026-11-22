// Package engine connects a profile through the system proxy: it builds and
// validates the Xray config, runs Xray, checks the tunnel and only then
// points the system proxy at our inbound. It implements the fail policy of
// spec §7.7: when the tunnel breaks the system proxy is reverted at once
// (fail-open for everything that is not protected) and applied again once
// Xray is healthy.
package engine

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/geo/assets"
	"github.com/ArtikGenius/zatvor/internal/geo/merge"
	"github.com/ArtikGenius/zatvor/internal/happ"
	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/ports"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/validate"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

const (
	stopTimeout     = 5 * time.Second
	driftInterval   = 5 * time.Second
	recheckInterval = 10 * time.Second
	switchRetry     = time.Second // an interrupted geo switch runs again
)

// Runner supervises one Xray process; *xrayrun.Runner implements it. After
// a GaveUp event the engine calls Stop.
type Runner interface {
	Start(ctx context.Context, cfg []byte, ready xrayrun.ReadyCheck) error
	Stop(timeout time.Duration) error
	Events() <-chan xrayrun.Event
}

// ProxyManager is the part of *sysproxy.Manager the engine uses.
type ProxyManager interface {
	Apply(ours sysapi.ProxyState, force bool) error
	Revert() error
	LastRevert() []journal.RevertResult
	Drifted() (bool, sysapi.ProxyState, error)
}

// Geo is the part of *assets.Assets the engine uses.
type Geo interface {
	Prepare(cfg []byte, pg *assets.ProfileGeo) (assetDir string, missing []merge.MissingTagError, err error)
	NeedsBootstrap(cfg []byte, pg assets.ProfileGeo) bool
	Fetch(ctx context.Context, pg assets.ProfileGeo, dial subfetch.Dialer) error
}

// Checker runs a tunnel check through dial (the probe inbound).
type Checker func(ctx context.Context, dial subfetch.Dialer) CheckResult

// BuildOptions are the settings that shape the generated config.
type BuildOptions struct {
	DevTools      bool
	AIDomains     []string
	UserBlocks    []string
	SelectedSites []string
	LogLevel      string
}

// Deps is everything the engine works with. Assets and Proxy are required;
// every nil func has the production default named on it.
type Deps struct {
	XrayPath string
	Assets   Geo
	Proxy    ProxyManager
	// NewRunner makes the runner of one Xray run with its geo set (default
	// xrayrun.New(XrayPath, assetDir, Clock)).
	NewRunner func(assetDir string) Runner
	// XrayTest validates a config (default validate.XrayTest with XrayPath).
	XrayTest func(ctx context.Context, cfg []byte, assetDir string) error
	// Ports re-validates the ports of the last connect (default ports.Pick
	// with ports.LoopbackProber); the bool reports a change.
	Ports func(prev gen.Ports) (gen.Ports, bool, error)
	// InitialPorts are the persisted ports.
	InitialPorts gen.Ports
	// SavePorts persists re-picked ports (optional).
	SavePorts func(gen.Ports) error
	// Ready waits until Xray serves the given ports (default: dial the
	// mixed and the probe inbound).
	Ready func(gen.Ports) xrayrun.ReadyCheck
	// Clock drives the drift poll and re-check timers (default wall clock).
	Clock xrayrun.Clock
	// Checker is the tunnel check (default TunnelCheck over
	// CheckServices).
	Checker Checker
	// CheckServices are the IP-check URLs (default DefaultCheckServices).
	CheckServices []string
	// Options returns the current build settings (default zero).
	Options func() BuildOptions
	// Safe is reported in State.
	Safe bool
}

func (d *Deps) defaults() {
	if d.Clock == nil {
		d.Clock = xrayrun.RealClock{}
	}
	if d.NewRunner == nil {
		path, clk := d.XrayPath, d.Clock
		d.NewRunner = func(dir string) Runner { return xrayrun.New(path, dir, clk) }
	}
	if d.XrayTest == nil {
		path := d.XrayPath
		d.XrayTest = func(ctx context.Context, cfg []byte, dir string) error {
			return validate.XrayTest(ctx, path, cfg, dir)
		}
	}
	if d.Ports == nil {
		var seed [8]byte
		_, _ = cryptorand.Read(seed[:])
		rnd := rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(seed[:]))))
		d.Ports = func(prev gen.Ports) (gen.Ports, bool, error) { return ports.Pick(prev, ports.LoopbackProber, rnd) }
	}
	if d.Ready == nil {
		d.Ready = func(p gen.Ports) xrayrun.ReadyCheck {
			mixed, probe := xrayrun.DialReady(loopback(p.Mixed)), xrayrun.DialReady(loopback(p.Probe))
			return func(ctx context.Context) error {
				if err := mixed(ctx); err != nil {
					return err
				}
				return probe(ctx)
			}
		}
	}
	if d.CheckServices == nil {
		d.CheckServices = DefaultCheckServices
	}
	if d.Checker == nil {
		svcs := d.CheckServices
		d.Checker = func(ctx context.Context, dial subfetch.Dialer) CheckResult { return TunnelCheck(ctx, dial, svcs) }
	}
	if d.Options == nil {
		d.Options = func() BuildOptions { return BuildOptions{} }
	}
}

// Engine runs one connection at a time. All methods are safe for
// concurrent use; operations are serialized.
type Engine struct {
	d      Deps
	events chan State

	// opMu serializes operations: Connect, Disconnect, ResolveConflict and
	// the session loop's handlers.
	opMu     sync.Mutex
	ports    gen.Ports        // ports of the last connect
	conflict *profile.Profile // the profile a proxy conflict stopped

	mu         sync.Mutex // guards the fields below; held briefly
	st         State
	sess       *session // written under opMu and mu
	netGen     uint64
	connSeq    uint64
	connCancel context.CancelCauseFunc
	// pendingConnects counts connects registered but not yet holding
	// opMu; while it is non-zero an interrupted work ctx stays cancelled.
	pendingConnects int

	// beforeOpLock is a test hook run by connect between registering
	// (number seq) and taking opMu.
	beforeOpLock func(seq uint64)
}

// session is one connected profile. Its fields are guarded by opMu;
// xrayUp is also written under mu (ProbeDialer reads it).
type session struct {
	ctx    context.Context // the loop's lifetime; cancelled by teardown and Disconnect
	cancel context.CancelFunc
	done   chan struct{} // closed when the loop has ended

	// work bounds the slow steps of one handler. A Connect waiting for
	// opMu cancels it (interruptWork, under mu); lock renews it. It is
	// always a child of ctx.
	work       context.Context
	workCancel context.CancelFunc

	prof      profile.Profile
	in        gen.Input // the profile's own build input (no bootstrap)
	pg        *assets.ProfileGeo
	cfg       []byte               // config running now
	dir       string               // its geo set
	runner    Runner               // the adopted runner (Start succeeded)
	events    <-chan xrayrun.Event // its events; nil when none is adopted
	bootstrap bool                 // running the bootstrap config
	xrayUp    bool                 // a started Xray is supposed to run
	applied   bool                 // the system proxy is ours
	// revertPending: a revert failed; the re-check timer retries it.
	revertPending bool
}

// New returns an Engine in phase Disconnected.
func New(d Deps) *Engine {
	d.defaults()
	return &Engine{
		d:      d,
		events: make(chan State, eventBuf),
		ports:  d.InitialPorts,
		st:     State{Phase: Disconnected, Ports: d.InitialPorts, Safe: d.Safe},
	}
}

func loopback(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// socksDialer dials through the authenticated probe inbound.
func socksDialer(port int, user, pass string) subfetch.Dialer {
	addr, auth := loopback(port), &proxy.Auth{User: user, Password: pass}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		d, err := proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
		if err != nil {
			return nil, err
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("engine: socks dialer has no context support")
		}
		return cd.DialContext(ctx, network, address)
	}
}

// ProbeDialer returns a dialer through the authenticated probe inbound
// (SOCKS5 with this connect's credentials). ok is false unless the phase is
// Connected, or Degraded while Xray runs.
func (e *Engine) ProbeDialer() (subfetch.Dialer, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.sess
	if s == nil || !s.xrayUp || (e.st.Phase != Connected && e.st.Phase != Degraded) {
		return nil, false
	}
	return socksDialer(s.in.Ports.Probe, s.in.ProbeUser, s.in.ProbePass), true
}

func (e *Engine) setSess(s *session) {
	e.mu.Lock()
	e.sess = s
	e.mu.Unlock()
}

func (e *Engine) setXrayUp(s *session, up bool) {
	e.mu.Lock()
	s.xrayUp = up
	e.mu.Unlock()
}

func (e *Engine) bumpGen() {
	e.mu.Lock()
	e.netGen++
	e.mu.Unlock()
}

// check runs the tunnel check through the probe inbound of in.
func (e *Engine) check(ctx context.Context, in gen.Input) CheckResult {
	e.mu.Lock()
	g := e.netGen
	e.mu.Unlock()
	res := e.d.Checker(ctx, socksDialer(in.Ports.Probe, in.ProbeUser, in.ProbePass))
	res.Gen, res.At = g, e.d.Clock.Now()
	return res
}

func checkError(res CheckResult) *Error {
	if res.Status == Bad {
		return &Error{Code: CodeCheckBad, Message: "IP-check services disagree on the tunnel's exit address"}
	}
	return &Error{Code: CodeCheckUnknown, Message: "fewer than two IP-check services answered through the tunnel"}
}

// profileGeo returns the geo URLs of the Happ routing profile the build
// uses for p, or nil.
func profileGeo(p profile.Profile) *assets.ProfileGeo {
	src := p.RoutingSource
	kind := src.Kind
	if kind == "" {
		kind = profile.SourceProvider
	}
	if src.Happ == nil || !(kind == profile.SourceHapp || (kind == profile.SourceProvider && p.Node != nil)) {
		return nil
	}
	ip, site := happ.GeoURLs(*src.Happ)
	if ip == "" && site == "" {
		return nil
	}
	return &assets.ProfileGeo{GeoIPURL: ip, GeoSiteURL: site}
}

// bootstrapInput is in with everything through the tunnel: the all-proxy
// preset (so no tag the bundled set lacks is referenced) plus the
// prepended bootstrap rule (spec §16 Q2.10, B3).
func bootstrapInput(in gen.Input) gen.Input {
	in.Profile.RoutingSource = profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}
	in.BootstrapAllProxy = true
	return in
}

// Connect connects p: pick ports, build (the bootstrap config when the
// profile's geo files must be downloaded first), prepare the geo set
// (missing tags fail here, before Xray starts), Schema and XrayTest, start
// Xray and wait until it is ready, run the tunnel check, and only when it
// is OK apply the system proxy. A running connection is torn down first.
// The error is an *Error, also kept in State.Err with phase Failed. On a
// proxy conflict Xray is stopped and ResolveConflict decides.
func (e *Engine) Connect(ctx context.Context, p profile.Profile) error {
	return e.connect(ctx, p, false)
}

// Causes of a cancelled connect.
var (
	errSuperseded   = errors.New("engine: a newer connect replaced this one")
	errDisconnected = errors.New("engine: disconnected")
)

// abortError is the error of a connect whose ctx ended.
func abortError(ctx context.Context) *Error {
	if errors.Is(context.Cause(ctx), errSuperseded) {
		return &Error{Code: CodeSuperseded, Message: "a newer connect replaced this one"}
	}
	return newError(CodeCanceled, "connect canceled", context.Cause(ctx), nil)
}

func (e *Engine) connect(ctx context.Context, p profile.Profile, force bool) error {
	if ctx.Err() != nil {
		// Dead on arrival: never registers, so it supersedes nothing and
		// leaves the connect in flight reachable by Disconnect.
		return abortError(ctx)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	e.mu.Lock()
	if e.connCancel != nil {
		e.connCancel(errSuperseded) // an older connect still in flight
	}
	if e.sess != nil {
		// Interrupt only its slow work (a recovery check, a geo prepare)
		// so opMu comes free; its loop keeps running until this connect
		// really replaces it.
		e.sess.interruptWork()
	}
	e.connSeq++
	seq := e.connSeq
	e.connCancel = cancel
	e.pendingConnects++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if e.connSeq == seq {
			e.connCancel = nil
		}
		e.mu.Unlock()
	}()

	if e.beforeOpLock != nil {
		e.beforeOpLock(seq)
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	e.pendingConnects--
	e.mu.Unlock()
	if ctx.Err() != nil {
		// Aborted before it began: a newer operation may already own the
		// state and the session, so touch neither.
		return abortError(ctx)
	}
	warns, rerr := e.teardownLocked()
	if rerr != nil {
		warns = append(warns, rerr)
	}
	e.conflict = nil
	e.update(func(s *State) {
		*s = State{Phase: Connecting, Profile: p.ID, Ports: e.ports, Safe: e.d.Safe}
		for _, w := range warns {
			s.warn(w)
		}
	})
	sess, err := e.establish(ctx, p, force)
	if err == nil && ctx.Err() != nil {
		// Cancelled while the last step ran: undo it (revert, then stop).
		e.setSess(sess)
		w, rerr := e.teardownLocked()
		e.conflict = nil
		for _, x := range append(w, rerr) {
			if x != nil {
				e.warn(x)
			}
		}
		err = abortError(ctx)
	}
	if err != nil {
		if ctx.Err() != nil && err.Code != CodeProxyConflict {
			err = abortError(ctx)
		}
		e.update(func(s *State) { s.Phase, s.Err = Failed, err })
		return err
	}
	e.setSess(sess)
	e.update(func(s *State) { s.Phase, s.Err, s.GeoBootstrap = Connected, nil, sess.bootstrap })
	go e.loop(sess)
	return nil
}

// establish runs the connect steps; on failure nothing of ours is left
// running or applied.
func (e *Engine) establish(ctx context.Context, p profile.Profile, force bool) (*session, *Error) {
	pp, changed, err := e.d.Ports(e.ports)
	if err != nil {
		return nil, newError(CodePortsUnavailable, "no free loopback ports", err, nil)
	}
	if changed {
		e.ports = pp
		if e.d.SavePorts != nil {
			if err := e.d.SavePorts(pp); err != nil {
				e.warn(newError(WarnPortsNotSaved, "re-picked ports were not saved", err, nil))
			}
		}
	}
	e.update(func(s *State) { s.Ports = pp })

	o := e.d.Options()
	in := gen.Input{Profile: p, Ports: pp, ProbeUser: randHex(16), ProbePass: randHex(16),
		DevTools: o.DevTools, AIDomains: o.AIDomains, UserBlocks: o.UserBlocks,
		SelectedSites: o.SelectedSites, LogLevel: o.LogLevel}
	cfg, _, err := gen.Build(in)
	if err != nil {
		return nil, profileError(CodeBuildFailed, "cannot build the Xray config", err, p)
	}
	pg := profileGeo(p)
	boot := pg != nil && e.d.Assets.NeedsBootstrap(cfg, *pg)
	if boot {
		if cfg, _, err = gen.Build(bootstrapInput(in)); err != nil {
			return nil, profileError(CodeBuildFailed, "cannot build the bootstrap config", err, p)
		}
	}
	dir, perr := e.prepare(ctx, cfg, pg)
	if perr != nil {
		return nil, perr
	}
	r := e.d.NewRunner(dir)
	if err := r.Start(ctx, cfg, e.d.Ready(pp)); err != nil {
		return nil, newError(CodeXrayStartFailed, "Xray did not start", err, cfg)
	}
	e.bumpGen()
	res := e.check(ctx, in)
	e.update(func(s *State) { s.Check = res })
	if res.Status != OK {
		_ = r.Stop(stopTimeout)
		return nil, checkError(res)
	}
	if ctx.Err() != nil {
		_ = r.Stop(stopTimeout)
		return nil, abortError(ctx)
	}
	if err := e.d.Proxy.Apply(sysproxy.Ours(pp.Mixed), force); err != nil {
		if ce, ok := asConflict(err); ok {
			_ = r.Stop(stopTimeout)
			e.conflict = &p
			return nil, conflictError(CodeProxyConflict, ce)
		}
		// Apply may have set some connections: revert before stopping.
		warns, rerr := e.revertLocked()
		_ = r.Stop(stopTimeout)
		for _, w := range append(warns, rerr) {
			if w != nil {
				e.warn(w)
			}
		}
		return nil, newError(CodeProxyApplyFailed, "cannot set the system proxy", err, nil)
	}
	sctx, scancel := context.WithCancel(context.Background())
	work, workCancel := context.WithCancel(sctx)
	s := &session{ctx: sctx, cancel: scancel, done: make(chan struct{}),
		work: work, workCancel: workCancel,
		prof: p, in: in, pg: pg, cfg: cfg, dir: dir,
		bootstrap: boot, xrayUp: true, applied: true}
	s.adopt(r)
	return s, nil
}

// prepare builds the geo set for cfg and validates cfg against it.
func (e *Engine) prepare(ctx context.Context, cfg []byte, pg *assets.ProfileGeo) (string, *Error) {
	dir, missing, err := e.d.Assets.Prepare(cfg, pg)
	if err != nil {
		return "", newError(CodeGeoPrepareFailed, "cannot prepare the geo databases", err, cfg)
	}
	if len(missing) > 0 {
		msgs := make([]string, len(missing))
		for i := range missing {
			msgs[i] = missing[i].Error()
		}
		return "", &Error{Code: CodeGeoMissingTag, Message: strings.Join(msgs, "; ")}
	}
	if _, err := validate.Schema(cfg); err != nil {
		return "", newError(CodeConfigInvalid, "the Xray config is invalid", err, cfg)
	}
	if err := e.d.XrayTest(ctx, cfg, dir); err != nil {
		return "", newError(CodeXrayTestFailed, "Xray rejected the config", err, cfg)
	}
	return dir, nil
}

// revertLocked reverts the system proxy. It returns a warning per
// connection someone else changed after us (left as they set it) and the
// revert error, if any.
func (e *Engine) revertLocked() ([]*Error, *Error) {
	var rerr *Error
	if err := e.d.Proxy.Revert(); err != nil {
		rerr = newError(CodeProxyRevertFailed, "cannot restore the previous system proxy", err, nil)
	}
	var warns []*Error
	for _, r := range e.d.Proxy.LastRevert() {
		if r.Outcome == journal.SkippedChangedByOther {
			warns = append(warns, &Error{Code: WarnProxyKeptForeign,
				Message: "another program changed the system proxy while we were connected; its setting was kept"})
			break
		}
	}
	return warns, rerr
}

// teardownLocked ends the current session: revert the proxy, then stop
// Xray. It does not wait for the session loop. The revert runs even when
// the proxy is not applied now: it only touches our pending journal
// entries, so it retries a revert that failed after a crash and is a no-op
// otherwise.
func (e *Engine) teardownLocked() ([]*Error, *Error) {
	s := e.sess
	if s == nil {
		return nil, nil
	}
	e.setSess(nil)
	s.cancel()
	warns, rerr := e.revertLocked()
	s.applied = false
	_ = s.runner.Stop(stopTimeout)
	e.setXrayUp(s, false)
	return warns, rerr
}

// Disconnect cancels a connect in progress, reverts the system proxy and
// then stops Xray. A failed revert leaves phase Failed with
// proxy_revert_failed and is returned.
func (e *Engine) Disconnect(ctx context.Context) error {
	e.mu.Lock()
	if e.connCancel != nil {
		e.connCancel(errDisconnected)
	}
	if e.sess != nil {
		e.sess.cancel()
	}
	e.mu.Unlock()

	e.opMu.Lock()
	s := e.sess
	e.conflict = nil
	warns, rerr := e.teardownLocked()
	e.update(func(st *State) {
		*st = State{Phase: Disconnected, Ports: e.ports, Safe: e.d.Safe}
		if rerr != nil {
			st.Phase, st.Err = Failed, rerr
		}
		for _, w := range warns {
			st.warn(w)
		}
	})
	e.opMu.Unlock()
	if s != nil {
		select {
		case <-s.done:
		case <-ctx.Done():
		}
	}
	if rerr != nil {
		return rerr
	}
	return nil
}

// ResolveConflict answers a proxy_conflict: force connects the same
// profile again and overwrites the foreign proxy; otherwise the engine
// disconnects and the foreign setting stays. It fails with no_conflict
// unless a conflict is pending.
func (e *Engine) ResolveConflict(ctx context.Context, force bool) error {
	e.opMu.Lock()
	p := e.conflict
	if p == nil {
		e.opMu.Unlock()
		return &Error{Code: CodeNoConflict, Message: "no system proxy conflict is pending"}
	}
	e.conflict = nil
	e.opMu.Unlock()
	if force {
		return e.connect(ctx, *p, true)
	}
	return e.Disconnect(ctx)
}
