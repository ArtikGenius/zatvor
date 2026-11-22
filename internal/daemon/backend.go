package daemon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/devtools"
	"github.com/ArtikGenius/zatvor/internal/diag"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/ports"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/settings"
	"github.com/ArtikGenius/zatvor/internal/store"
	"github.com/ArtikGenius/zatvor/internal/subsvc"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

// codeShuttingDown: an operation arrived after the daemon began to shut
// down; nothing reached the services.
const codeShuttingDown = "shutting_down"

var errStopping = &api.Error{Code: codeShuttingDown, Message: "the daemon is shutting down"}

// stateRetry is how soon a failed dev-tool state-file write is tried again.
const stateRetry = time.Second

// logLevels are the Xray log levels gen.Build accepts.
var logLevels = map[string]bool{"debug": true, "info": true, "warning": true, "error": true, "none": true}

// backend is the api.Backend over the daemon's services. It is the single
// reader of the engine's and subsvc's event channels (pump) and fans them
// out to the API's subscribers.
type backend struct {
	logf         func(string, ...any)
	settingsPath string
	stateFile    string
	eng          *engine.Engine
	svc          *subsvc.Service
	store        *guardedStore // read by Diag only: the redaction needs the full URLs
	dev          *devtools.Manager
	fan          *fanout
	onShow       func()
	clock        xrayrun.Clock
	putState     func(path, v string) error // writeState; tests inject failures
	version      string                     // zatvord's version, for the diagnostics
	journal      *journal.Journal           // the mode's journal, for the diagnostics
	logPath      string                     // the daemon log, for the diagnostics

	setMu sync.Mutex // guards st and the settings file
	st    settings.Settings

	pickMu sync.Mutex
	rnd    *mrand.Rand

	devMu   sync.Mutex // serializes dev-tool changes
	devOn   atomic.Bool
	devPort int // the port the dev tools were last enabled with

	// gate: operations hold it shared; shutdown takes it exclusively to
	// set closing, after cancelling ops.
	gate    sync.RWMutex
	closing bool
	ops     context.Context
	stopOps context.CancelFunc
}

func newBackend(d *daemon, settingsPath string, st settings.Settings, stateFile string, sys *system) *backend {
	var seed [8]byte
	_, _ = rand.Read(seed[:])
	ops, stop := context.WithCancel(context.Background())
	put := writeState
	if d.opts.hooks.writeState != nil {
		put = d.opts.hooks.writeState
	}
	return &backend{
		clock: d.opts.Clock, putState: put,
		logf: d.logf, settingsPath: settingsPath, stateFile: stateFile, dev: sys.dev,
		version: d.opts.Version, journal: sys.journal, logPath: filepath.Join(d.opts.Dirs.Logs, LogFile),
		fan: newFanout(), onShow: d.opts.hooks.onShow, st: st,
		rnd: mrand.New(mrand.NewSource(int64(binary.LittleEndian.Uint64(seed[:])))),
		ops: ops, stopOps: stop,
	}
}

var _ api.Backend = (*backend)(nil)

// ---- settings ----

func (b *backend) settings() settings.Settings {
	b.setMu.Lock()
	defer b.setMu.Unlock()
	return b.st
}

func (b *backend) saveSettings(edit func(*settings.Settings)) error {
	b.setMu.Lock()
	defer b.setMu.Unlock()
	next := b.st
	edit(&next)
	if next == b.st {
		return nil
	}
	if err := settings.Save(b.settingsPath, next); err != nil {
		return err
	}
	b.st = next
	return nil
}

func (b *backend) pick(prev gen.Ports) (gen.Ports, bool, error) {
	b.pickMu.Lock()
	defer b.pickMu.Unlock()
	return ports.Pick(prev, ports.LoopbackProber, b.rnd)
}

// ensurePorts picks the ports a fresh install has none of yet, so the
// dev-tool port is known before the first connect.
func (b *backend) ensurePorts() {
	p := b.settings().Ports
	if p.Mixed != 0 && p.Probe != 0 && p.DevTools != 0 && p.API != 0 {
		return
	}
	np, _, err := b.pick(p)
	if err != nil {
		b.logf("ports: no free loopback ports")
		return
	}
	if err := b.savePorts(np); err != nil {
		b.logf("settings: cannot save the ports")
	}
}

// enginePorts re-validates the persisted ports at each connect, so ports
// changed through the API take effect on the next connect.
func (b *backend) enginePorts(prev gen.Ports) (gen.Ports, bool, error) {
	np, changed, err := b.pick(b.settings().Ports)
	return np, changed || np != prev, err
}

func (b *backend) savePorts(p gen.Ports) error {
	return b.saveSettings(func(s *settings.Settings) { s.Ports = p })
}

func (b *backend) buildOptions() engine.BuildOptions {
	s := b.settings()
	return engine.BuildOptions{DevTools: s.DevTools || b.devOn.Load(), LogLevel: s.LogLevel}
}

// ---- the operation gate ----

// enter starts an operation: it fails once the daemon is stopping, and the
// returned ctx is also cancelled when shutdown begins.
func (b *backend) enter(ctx context.Context) (context.Context, func(), error) {
	b.gate.RLock()
	if b.closing {
		b.gate.RUnlock()
		return ctx, func() {}, errStopping
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(b.ops, cancel)
	return ctx, func() { stop(); cancel(); b.gate.RUnlock() }, nil
}

// close ends every operation and the connection: running operations are
// cancelled, the engine disconnects, and once no operation is left a
// second Disconnect tears down whatever one of them still started.
func (b *backend) close(ctx context.Context) {
	b.stopOps()
	if err := b.eng.Disconnect(ctx); err != nil {
		b.logf("engine: disconnect on exit: %s", engineCode(err))
	}
	b.gate.Lock()
	b.closing = true
	b.gate.Unlock()
	if b.eng.State().Phase != engine.Disconnected {
		if err := b.eng.Disconnect(ctx); err != nil {
			b.logf("engine: disconnect on exit: %s", engineCode(err))
		}
	}
}

func engineCode(err error) string {
	var ee *engine.Error
	if errors.As(err, &ee) {
		return ee.Code
	}
	return "error"
}

// ---- api.Backend ----

func (b *backend) Status() engine.State { return b.eng.State() }

func (b *backend) Subscribe(ctx context.Context) <-chan api.Event { return b.fan.subscribe(ctx) }

func (b *backend) Profiles() []profile.Profile {
	var out []profile.Profile
	for _, sub := range b.svc.List() {
		out = append(out, sub.Profiles...)
	}
	return out
}

func (b *backend) Connect(ctx context.Context, profileID string) error {
	ctx, done, err := b.enter(ctx)
	defer done()
	if err != nil {
		return err
	}
	var p *profile.Profile
	for _, cand := range b.Profiles() {
		if cand.ID == profileID {
			p = &cand
			break
		}
	}
	if p == nil {
		return &api.Error{Code: api.CodeNotFound, Message: "no such profile"}
	}
	if err := b.eng.Connect(ctx, *p); err != nil {
		return err
	}
	if err := b.saveSettings(func(s *settings.Settings) { s.ActiveProfile = profileID }); err != nil {
		b.logf("settings: cannot save the active profile")
	}
	return nil
}

func (b *backend) Disconnect(ctx context.Context) error {
	ctx, done, err := b.enter(ctx)
	defer done()
	if err != nil {
		return err
	}
	return b.eng.Disconnect(ctx)
}

func (b *backend) ResolveConflict(ctx context.Context, force bool) error {
	ctx, done, err := b.enter(ctx)
	defer done()
	if err != nil {
		return err
	}
	return b.eng.ResolveConflict(ctx, force)
}

func (b *backend) Subscriptions() []profile.Subscription { return b.svc.List() }

func (b *backend) AddSubscription(ctx context.Context, rawURL, ua string) (profile.Subscription, profile.Diff, error) {
	ctx, done, err := b.enter(ctx)
	defer done()
	if err != nil {
		return profile.Subscription{}, profile.Diff{}, err
	}
	return b.svc.Add(ctx, rawURL, ua)
}

func (b *backend) UpdateSubscription(ctx context.Context, id string) (profile.Diff, error) {
	ctx, done, err := b.enter(ctx)
	defer done()
	if err != nil {
		return profile.Diff{}, err
	}
	return b.svc.Update(ctx, id, subsvc.User)
}

func (b *backend) RemoveSubscription(id string) error {
	_, done, err := b.enter(context.Background())
	defer done()
	if err != nil {
		return err
	}
	return b.svc.Remove(id)
}

func (b *backend) Settings() settings.Settings { return b.settings() }

func badRequest(format string, args ...any) error {
	return &api.Error{Code: api.CodeBadRequest, Message: fmt.Sprintf(format, args...)}
}

// PutSettings replaces the whole settings document after validating it. A
// zero port (Xray's or the local API's) keeps the current one. Changed
// ports take effect on the next connect; DevTools takes effect through
// SetDevTools or at the next start.
func (b *backend) PutSettings(s settings.Settings) (settings.Settings, error) {
	_, done, err := b.enter(context.Background())
	defer done()
	if err != nil {
		return settings.Settings{}, err
	}
	b.setMu.Lock()
	defer b.setMu.Unlock()
	cur := b.st
	if s.Version != settings.CurrentVersion {
		return settings.Settings{}, badRequest("version must be %d", settings.CurrentVersion)
	}
	keep := func(p *int, c int) {
		if *p == 0 {
			*p = c
		}
	}
	keep(&s.Ports.Mixed, cur.Ports.Mixed)
	keep(&s.Ports.Probe, cur.Ports.Probe)
	keep(&s.Ports.DevTools, cur.Ports.DevTools)
	keep(&s.Ports.API, cur.Ports.API)
	keep(&s.LocalAPIPort, cur.LocalAPIPort)
	xray := []int{s.Ports.Mixed, s.Ports.Probe, s.Ports.DevTools, s.Ports.API}
	seen := map[int]bool{}
	for _, p := range xray {
		if p < 1 || p > 65535 {
			return settings.Settings{}, badRequest("ports must be between 1 and 65535")
		}
		if seen[p] {
			return settings.Settings{}, badRequest("ports must be distinct")
		}
		seen[p] = true
	}
	if s.LocalAPIPort != 0 && (s.LocalAPIPort < 1 || s.LocalAPIPort > 65535 || seen[s.LocalAPIPort]) {
		return settings.Settings{}, badRequest("localApiPort must be a port none of Xray's ports uses")
	}
	if s.LogLevel != "" && !logLevels[s.LogLevel] {
		return settings.Settings{}, badRequest("logLevel must be one of debug, info, warning, error, none")
	}
	if err := settings.Save(b.settingsPath, s); err != nil {
		return settings.Settings{}, err
	}
	b.st = s
	return s, nil
}

func (b *backend) SetDevTools(on bool) ([]devtools.Target, error) {
	_, done, err := b.enter(context.Background())
	defer done()
	if err != nil {
		return nil, err
	}
	return b.setDevTools(on)
}

// setDevTools turns dev-tool routing on or off and saves the choice. On, it
// uses the connected dev-tool port, else the persisted one.
func (b *backend) setDevTools(on bool) ([]devtools.Target, error) {
	b.devMu.Lock()
	defer b.devMu.Unlock()
	var err error
	if on {
		port := b.settings().Ports.DevTools
		if st := b.eng.State(); st.Phase == engine.Connected && st.Ports.DevTools != 0 {
			port = st.Ports.DevTools
		}
		err = b.dev.Enable(port)
		b.devOn.Store(true)
		b.devPort = port
	} else {
		err = b.dev.Disable()
		b.devOn.Store(false)
		b.devPort = 0
	}
	if serr := b.saveSettings(func(s *settings.Settings) { s.DevTools = on }); serr != nil {
		b.logf("settings: cannot save the dev-tool setting")
		if err == nil {
			err = serr
		}
	}
	return b.dev.Status(), err
}

// followDevPort enables the dev tools again when a connect re-picked the
// dev-tool port: the settings keys name the port itself.
func (b *backend) followDevPort(port int) {
	b.devMu.Lock()
	defer b.devMu.Unlock()
	if !b.devOn.Load() || b.devPort == port {
		return
	}
	if err := b.dev.Enable(port); err != nil {
		b.logf("devtools: follow the new port: %s", devCode(err))
	}
	b.devPort = port
}

// devToolsExit turns the dev tools off when they were on in this process
// but the setting is off (on for this session only). With the setting on,
// the files are reverted by the exit RevertAll and enabled again at the
// next start; the wrappers stay.
func (b *backend) devToolsExit() {
	b.devMu.Lock()
	defer b.devMu.Unlock()
	if !b.devOn.Load() || b.settings().DevTools {
		return
	}
	if err := b.dev.Disable(); err != nil {
		b.logf("devtools: disable on exit: %s", devCode(err))
	}
	b.devOn.Store(false)
}

func (b *backend) Show() {
	b.logf("a second launch asked to show the window")
	if b.onShow != nil {
		b.onShow()
	}
}

// Check runs a fresh tunnel check through the connected tunnel.
func (b *backend) Check(ctx context.Context) (engine.CheckResult, error) {
	ctx, done, err := b.enter(ctx)
	defer done()
	if err != nil {
		return engine.CheckResult{}, err
	}
	return b.eng.Recheck(ctx)
}

// Diag writes the diagnostics zip: versions, settings, the mode's journal,
// the end of the daemon log, the last Xray output, the state and the
// subscription hosts, with every secret value of the stored subscriptions
// redacted (diag.Bundle).
func (b *backend) Diag(ctx context.Context, w io.Writer) error {
	_, done, err := b.enter(ctx)
	defer done()
	if err != nil {
		return err
	}
	jb, err := b.journal.Bytes()
	if err != nil {
		b.logf("diag: cannot read the journal")
	}
	// svc.List reduces URLs to their host; the redaction needs them whole.
	// An unreadable store holds no secret this process knows, so the
	// bundle is still made, without subscriptions.
	subs, err := b.store.Load()
	if err != nil {
		b.logf("diag: cannot read the profile store (%s)", storeCode(err))
	}
	return diag.Bundle(w, diag.Sources{
		Versions:      diag.CurrentVersions(b.version),
		Settings:      b.settings(),
		Journal:       jb,
		DaemonLog:     readLog(b.logPath),
		XrayOutput:    b.eng.XrayOutput(),
		State:         b.eng.State(),
		Subscriptions: subs,
		Now:           b.clock.Now(),
	})
}

// readLog is the daemon log with its rotated part (R44) before it; a
// missing part is left out.
func readLog(path string) []byte {
	var out []byte
	for _, p := range []string{path + ".1", path} {
		if b, err := os.ReadFile(p); err == nil {
			out = append(out, b...)
			if len(b) > 0 && b[len(b)-1] != '\n' {
				out = append(out, '\n')
			}
		}
	}
	return out
}

// updateOnStart sends one direct (or tunnel, when up) request per
// subscription, never retried (UpdateOnStart, off by default).
func (b *backend) updateOnStart(ctx context.Context) {
	for _, sub := range b.svc.List() {
		if ctx.Err() != nil {
			return
		}
		if _, err := b.svc.Update(ctx, sub.ID, subsvc.Start); err != nil {
			var se *subsvc.Error
			code := "error"
			if errors.As(err, &se) {
				code = se.Code
			}
			b.logf("subscriptions: start update failed (%s)", code)
		}
	}
}

// ---- events ----

// pump is the single reader of the engine's and subsvc's events. It fans
// them out, reports new warnings, and keeps the dev-tool state file (R23):
// the proxy URL while connected, empty in every other phase. A failed
// write is tried again every stateRetry until it succeeds, so the file
// never keeps a stale URL until the next engine event.
func (b *backend) pump(ctx context.Context) {
	states, subs := b.eng.Events(), b.svc.Events()
	last, want := "", "" // the state file was emptied at start
	var retry <-chan time.Time
	write := func() {
		if want == last {
			retry = nil
			return
		}
		if err := b.putState(b.stateFile, want); err != nil {
			if retry == nil {
				b.logf("devtools: cannot write the state file; retrying")
			}
			retry = b.clock.After(stateRetry)
			return
		}
		last, retry = want, nil
	}
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry:
			retry = nil
			write()
		case e := <-subs:
			b.fan.publish(api.SubscriptionEvent(e))
		case s := <-states:
			b.fan.publish(api.StateEvent(s))
			cur := map[string]bool{}
			for _, w := range s.Warnings {
				cur[w.Code] = true
				if !seen[w.Code] {
					b.fan.publish(api.WarningEvent(w))
				}
			}
			seen = cur
			want = ""
			if s.Phase == engine.Connected && s.Ports.DevTools != 0 {
				want = "http://127.0.0.1:" + strconv.Itoa(s.Ports.DevTools)
				b.followDevPort(s.Ports.DevTools)
			}
			if retry == nil {
				write()
			}
		}
	}
}

// writeState writes the dev-tool state file. A shell reading it at that
// moment can make the rename fail on Windows (a sharing violation), so it
// tries a few times.
func writeState(path, v string) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = writeAtomic(path, []byte(v)); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}

// fanoutBuffer is each subscriber's queue; a subscriber that falls this far
// behind is closed.
const fanoutBuffer = 64

// fanout delivers events to every subscriber without ever blocking.
type fanout struct {
	mu     sync.Mutex
	next   int
	subs   map[int]chan api.Event
	closed bool
}

func newFanout() *fanout { return &fanout{subs: map[int]chan api.Event{}} }

func (f *fanout) subscribe(ctx context.Context) <-chan api.Event {
	ch := make(chan api.Event, fanoutBuffer)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		close(ch)
		return ch
	}
	id := f.next
	f.next++
	f.subs[id] = ch
	context.AfterFunc(ctx, func() { f.drop(id) })
	return ch
}

func (f *fanout) drop(id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.subs[id]; ok {
		delete(f.subs, id)
		close(ch)
	}
}

// publish queues e for every subscriber; a full one is closed (it then
// reconnects and gets the current state first).
func (f *fanout) publish(e api.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, ch := range f.subs {
		select {
		case ch <- e:
		default:
			delete(f.subs, id)
			close(ch)
		}
	}
}

func (f *fanout) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for id, ch := range f.subs {
		delete(f.subs, id)
		close(ch)
	}
}

// ---- the store guard ----

var errStoreClosed = errors.New("daemon: the profile store is closed")

// guardedStore is the subsvc.Store over the store. After Close, which waits
// for a Load or Save in progress, nothing reads or writes the store, so a
// detached subscription flight can never save during or after shutdown.
type guardedStore struct {
	mu     sync.RWMutex
	s      *store.Store
	closed bool
}

func (g *guardedStore) Load() ([]profile.Subscription, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return nil, errStoreClosed
	}
	return g.s.Load()
}

func (g *guardedStore) Save(subs []profile.Subscription) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return errStoreClosed
	}
	return g.s.Save(subs)
}

func (g *guardedStore) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}
