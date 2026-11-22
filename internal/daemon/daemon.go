// Package daemon assembles zatvord: the single-instance lock, the journal
// revert of a previous run, settings, the encrypted profile store, the
// connection engine, the subscription service, developer-tool routing and
// the local API.
//
// In safe mode (the default) every system change goes to a recorder that
// appends to Logs/safe-mode.jsonl, so nothing outside the data directory is
// written. The daemon installs nothing system-wide (R41): its only system
// changes in live mode are the system proxy, through the engine, and the
// developer-tool files when the user turns that feature on.
//
// The two modes never share state that a system change depends on: each
// has its own journal (Journal/safe, Journal/live; R16) and its own profile
// store (Data/store/safe, Data/store/live; R43). The safe store's key is a
// DPAPI-sealed file (store/safe.key; a plain key file off Windows), so a
// safe start never needs Credential Manager. Only the live store uses
// Credential Manager, after the one-time move of a DPAPI key file
// store/live.key into it (R17).
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/devtools"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/geo/assets"
	"github.com/ArtikGenius/zatvor/internal/hwid"
	"github.com/ArtikGenius/zatvor/internal/instance"
	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/rotlog"
	"github.com/ArtikGenius/zatvor/internal/safemode"
	"github.com/ArtikGenius/zatvor/internal/settings"
	"github.com/ArtikGenius/zatvor/internal/store"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/ArtikGenius/zatvor/internal/subsvc"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

// Names inside the data directories.
const (
	SettingsFile  = appdir.SettingsFile // in Dirs.Data
	StoreDir      = "store"             // in Dirs.Data: <mode>/ holds a store, <mode>.key its key file
	BinDir        = "bin"               // in Dirs.Data: the dev-tool wrappers
	SafeModeLog   = "safe-mode.jsonl"   // in Dirs.Logs: what safe mode would have changed
	StateFileName = "devtools.proxy"    // in Dirs.Run: the dev-tool proxy URL while connected
	LogFile       = "zatvord.log"       // in Dirs.Logs: the daemon log (zatvord writes it, the diagnostics read it)
)

// ErrLiveRefused: live mode needs Windows and env ZATVOR_ALLOW_LIVE=1
// (ADR 0004); nothing was started.
var ErrLiveRefused = errors.New("daemon: live mode refused (it needs Windows and ZATVOR_ALLOW_LIVE=1)")

const (
	forwardWait     = 5 * time.Second  // a second start retries Forward this long
	discoveryCheck  = 5 * time.Second  // how often the discovery files are checked (K34)
	shutdownTimeout = 20 * time.Second // bounds Disconnect on exit
	fetchTimeout    = 20 * time.Second // one subscription request
)

// Options configure Run.
type Options struct {
	Dirs       appdir.Dirs
	Mode       safemode.Mode
	XrayPath   string // the Xray binary
	BundledGeo string // the bundled geo directory (tools/geofetch output)
	Version    string
	// Clock drives the engine, the subscription scheduler and the
	// discovery check. Nil: the wall clock.
	Clock xrayrun.Clock
	// Env looks up environment variables (the user's directories, the live
	// gate). Nil: os.Getenv.
	Env func(string) string
	// Log receives the daemon log. It never holds secrets. Nil: discarded.
	Log io.Writer
	// CheckServices are the IP-check URLs of the tunnel check (R6). Empty:
	// the engine's default public services. zatvord never sets it; it
	// exists for the live acceptance tests, which check against local echo
	// services. No flag or environment variable reaches it.
	CheckServices []string

	hooks *hooks // tests only
}

// devRoots are the user directories dev-tool files live under.
type devRoots struct{ Home, Documents, AppData string }

// hooks replace dependencies in tests.
type hooks struct {
	sysProxy  sysapi.SystemProxy // replaces the mode's system proxy
	roots     *devRoots          // replaces the user's directories
	lockName  string             // replaces instance.Name
	machineID string
	engine    func(*engine.Deps)
	started   func(addr string) // after the API started
	// writeState replaces the dev-tool state-file write of the pump.
	writeState func(path, v string) error
	// storeOpened is told the store directory and key file openStore uses.
	storeOpened func(dir, key string)
	onShow      func()
}

func (o *Options) defaults() {
	if o.Clock == nil {
		o.Clock = xrayrun.RealClock{}
	}
	if o.Env == nil {
		o.Env = os.Getenv
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.hooks == nil {
		o.hooks = &hooks{}
	}
}

func liveGate(o Options) error {
	if o.Mode != safemode.Live {
		return nil
	}
	if !liveSupported || o.Env("ZATVOR_ALLOW_LIVE") != "1" {
		return ErrLiveRefused
	}
	return nil
}

// Run runs the daemon until ctx ends. If another instance holds the lock,
// it asks that one to show its window and returns nil. The start order is:
// the lock, emptying the dev-tool state file, the journal revert of the
// previous run, settings, the store, the services, the API, and then the
// subscription refresh when the user turned UpdateOnStart on. On exit it
// disconnects, turns dev tools off when they were on for this session only,
// reverts the journal and stops the API.
func Run(ctx context.Context, opts Options) error {
	opts.defaults()
	if opts.Dirs.Data == "" || opts.Dirs.Run == "" || opts.Dirs.Journal == "" || opts.Dirs.Logs == "" {
		return errors.New("daemon: the data directories are not set")
	}
	if err := liveGate(opts); err != nil {
		return err
	}
	l := log.New(opts.Log, "zatvord: ", log.LstdFlags|log.Lmsgprefix)
	d := &daemon{opts: opts, logf: l.Printf}

	name := opts.hooks.lockName
	if name == "" {
		name = instance.Name(opts.Dirs)
	}
	release, already, err := instance.Acquire(name)
	if err != nil {
		return err
	}
	if already {
		d.logf("another instance is running; asking it to show its window")
		return Forward(ctx, opts.Dirs)
	}
	defer release()
	return d.run(ctx)
}

// Forward asks the running instance to show its window. It retries for
// about 5 s, because that instance may still be starting and not have
// written its discovery file yet.
func Forward(ctx context.Context, dirs appdir.Dirs) error {
	deadline := time.Now().Add(forwardWait)
	for {
		err := instance.Forward(dirs)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon: another instance is running but does not answer: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

type daemon struct {
	opts    Options
	logf    func(string, ...any)
	apiAddr string // the address of the last API start; run goroutine only
}

func (d *daemon) run(ctx context.Context) error {
	o := d.opts
	stateFile := filepath.Join(o.Dirs.Run, StateFileName)
	// R23: nothing of a previous run may route dev tools to a dead port.
	if err := writeState(stateFile, ""); err != nil {
		return fmt.Errorf("daemon: empty the dev-tool state file: %w", err)
	}
	sys, err := openSystem(o)
	if err != nil {
		return err
	}
	d.logReverts("start", sys.journal.RevertAll(sys.reverters()))

	settingsPath := filepath.Join(o.Dirs.Data, SettingsFile)
	st, err := settings.Load(settingsPath)
	switch {
	case errors.Is(err, settings.ErrCorrupt):
		d.logf("settings: the file is damaged; using the defaults")
		st = settings.Defaults()
	case err != nil:
		return fmt.Errorf("daemon: %w", err)
	}

	raw, err := openStore(o, d.logf)
	if err != nil {
		return err
	}
	gs := &guardedStore{s: raw}
	if _, err := gs.Load(); err != nil {
		d.logf("store: cannot read the profile store (%s)", storeCode(err))
	}

	b := newBackend(d, settingsPath, st, stateFile, sys, gs)
	b.ensurePorts()

	ed := engine.Deps{
		XrayPath:     o.XrayPath,
		Assets:       assets.New(o.BundledGeo, filepath.Join(o.Dirs.Geo, "cache"), filepath.Join(o.Dirs.Geo, "sets")),
		Proxy:        sys.proxy,
		Ports:        b.enginePorts,
		InitialPorts: b.settings().Ports,
		SavePorts:    b.savePorts,
		Clock:        o.Clock,
		Options:      b.buildOptions,
		Safe:         o.Mode == safemode.Safe,
	}
	if len(o.CheckServices) > 0 {
		ed.CheckServices = append([]string(nil), o.CheckServices...)
	}
	if o.hooks.engine != nil {
		o.hooks.engine(&ed)
	}
	b.eng = engine.New(ed)

	sd := subsvc.Deps{
		Fetcher:   subfetch.New(fetchTimeout),
		Store:     gs,
		Tunnel:    b.eng,
		MachineID: d.machineID(),
		Device:    hwid.Device{OS: runtime.GOOS},
		Clock:     o.Clock,
		Version:   o.Version,
		OS:        runtime.GOOS,
		Logf:      d.logf,
	}
	b.svc = subsvc.New(sd)

	pumpCtx, stopPump := context.WithCancel(context.Background())
	pumpDone := make(chan struct{})
	go func() { defer close(pumpDone); b.pump(pumpCtx) }()

	if b.settings().DevTools {
		if _, err := b.setDevTools(true); err != nil {
			d.logf("devtools: enable at start: %s", devCode(err))
		}
	}

	srv := api.New(b, o.Dirs)
	svcCtx, stopSvc := context.WithCancel(context.Background())
	svcDone := make(chan struct{})
	exit := func() {
		d.shutdown(b, srv, stopSvc, svcDone, gs, stopPump, pumpDone, sys)
	}
	if err := d.startAPI(ctx, srv, b); err != nil {
		close(svcDone)
		exit()
		return err
	}
	if o.hooks.started != nil {
		o.hooks.started(d.apiAddr)
	}
	go func() {
		defer close(svcDone)
		if b.settings().UpdateOnStart {
			b.updateOnStart(svcCtx)
		}
		b.svc.Run(svcCtx)
	}()

	for {
		select {
		case <-ctx.Done():
			exit()
			return nil
		case <-o.Clock.After(discoveryCheck):
			d.checkDiscovery(srv)
		}
	}
}

// startAPI starts the API and persists its port in LocalAPIPort (never in
// the Xray ports).
func (d *daemon) startAPI(ctx context.Context, srv *api.Server, b *backend) error {
	addr, err := srv.Start(ctx)
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	d.apiAddr = addr
	_, ps, _ := net.SplitHostPort(addr)
	if port, _ := strconv.Atoi(ps); port != 0 && port != b.settings().LocalAPIPort {
		if err := b.saveSettings(func(s *settings.Settings) { s.LocalAPIPort = port }); err != nil {
			d.logf("settings: cannot save the local API port")
		}
	}
	return nil
}

// checkDiscovery writes the API's discovery and token files again when
// they disappeared while it runs (K34), so that clients find it again. The
// listener keeps running, so operations in flight are not cancelled (R57).
func (d *daemon) checkDiscovery(srv *api.Server) {
	_, derr := os.Stat(filepath.Join(d.opts.Dirs.Run, api.DiscoveryFile))
	_, terr := os.Stat(filepath.Join(d.opts.Dirs.Run, api.TokenFile))
	if derr == nil && terr == nil {
		return
	}
	d.logf("api: the discovery files are gone; writing them again")
	if err := srv.Republish(); err != nil {
		d.logf("api: cannot write the discovery files")
	}
}

func (d *daemon) shutdown(b *backend, srv *api.Server, stopSvc context.CancelFunc, svcDone <-chan struct{},
	gs *guardedStore, stopPump context.CancelFunc, pumpDone <-chan struct{}, sys *system) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	b.close(ctx)
	// No subscription flight may save once the store is closed.
	stopSvc()
	<-svcDone
	gs.Close()
	b.devToolsExit()
	stopPump()
	<-pumpDone
	if err := writeState(b.stateFile, ""); err != nil {
		d.logf("devtools: cannot empty the state file")
	}
	d.logReverts("exit", sys.journal.RevertAll(sys.reverters()))
	srv.Stop()
	b.fan.close()
}

func (d *daemon) machineID() string {
	if d.opts.hooks.machineID != "" {
		return d.opts.hooks.machineID
	}
	id, err := hwid.MachineID()
	if err != nil {
		d.logf("hwid: the machine id is unavailable")
		return ""
	}
	return id
}

// logReverts logs each result with its kind, a target label without paths
// or names, its outcome and an error code.
func (d *daemon) logReverts(when string, rs []journal.RevertResult) {
	for _, r := range rs {
		d.logf("%s revert: %s %s: %s%s", when, r.Kind, targetLabel(r.Entry), r.Outcome, revertCode(r.Err))
	}
}

func targetLabel(e journal.Entry) string {
	name, _, _ := strings.Cut(e.Target, ":")
	if e.Kind == sysproxy.Kind || e.Kind == devtools.Kind {
		return name // "lan", "ras", or a dev-tool name
	}
	return "-"
}

func revertCode(err error) string {
	var de *devtools.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &de):
		return " (" + de.Code + ")"
	case errors.Is(err, journal.ErrNoReverter):
		return " (no_reverter)"
	case errors.Is(err, journal.ErrBlockedByNewer):
		return " (blocked_by_newer)"
	case errors.Is(err, journal.ErrChangedByOther):
		return " (changed_by_other)"
	default:
		return " (error)"
	}
}

func devCode(err error) string {
	var de *devtools.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return "error"
}

func storeCode(err error) string {
	for _, c := range []struct {
		err  error
		code string
	}{{store.ErrKey, "key"}, {store.ErrWrongKey, "wrong_key"}, {store.ErrCorrupt, "corrupt"},
		{store.ErrFormat, "format"}, {store.ErrVersion, "version"}} {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	return "io"
}

// system is the journal and the system-changing managers of one mode.
type system struct {
	journal *journal.Journal
	proxy   *sysproxy.Manager
	dev     *devtools.Manager
}

// openSystem opens the journal of the mode (R16: Journal/safe or
// Journal/live) and the managers over the mode's system proxy and file
// system: the live ones, or in safe mode recorders writing to
// Logs/safe-mode.jsonl.
func openSystem(o Options) (*system, error) {
	if err := liveGate(o); err != nil {
		return nil, err
	}
	var (
		sp   sysapi.SystemProxy
		fsys devtools.FS
	)
	if o.Mode == safemode.Live {
		p, err := liveSystemProxy()
		if err != nil {
			return nil, err
		}
		sp, fsys = p, devtools.OSFS()
	} else {
		// It rotates like zatvord.log (R44); the supervisor writes it too.
		w := rotlog.New(filepath.Join(o.Dirs.Logs, SafeModeLog), rotlog.Max)
		if _, err := w.Write(nil); err != nil {
			return nil, fmt.Errorf("daemon: open the safe-mode log: %w", err)
		}
		sp, fsys = sysapi.NewRecorder(w), devtools.NewRecorder(w)
	}
	if o.hooks != nil && o.hooks.sysProxy != nil {
		sp = o.hooks.sysProxy
	}
	j, err := journal.Open(filepath.Join(o.Dirs.Journal, o.Mode.String()))
	if err != nil {
		return nil, fmt.Errorf("daemon: open the journal: %w", err)
	}
	return &system{journal: j, proxy: sysproxy.New(sp, j), dev: devtools.New(devToolRoots(o), j, fsys)}, nil
}

func (s *system) reverters() map[string]journal.Reverter {
	return map[string]journal.Reverter{sysproxy.Kind: s.proxy.Reverter(), devtools.Kind: s.dev.Reverter()}
}

// RevertSet is what the supervisor needs to revert in the daemon's place.
type RevertSet struct {
	// Journal is the journal of the mode (R16), with the daemon's
	// Reverters.
	Journal   *journal.Journal
	Reverters map[string]journal.Reverter
	// Cleanup runs after the revert: it empties the dev-tool state file,
	// so no shell or wrapper routes to the dead daemon's port (R23).
	Cleanup func() error
}

// Reverters opens the journal of opts.Mode and returns it with the
// reverters and the cleanup the daemon uses, for the supervisor's revert.
func Reverters(opts Options) (*RevertSet, error) {
	opts.defaults()
	sys, err := openSystem(opts)
	if err != nil {
		return nil, err
	}
	stateFile := filepath.Join(opts.Dirs.Run, StateFileName)
	return &RevertSet{
		Journal: sys.journal, Reverters: sys.reverters(),
		Cleanup: func() error { return writeState(stateFile, "") },
	}, nil
}

func devToolRoots(o Options) devtools.Roots {
	var r devRoots
	if o.hooks != nil && o.hooks.roots != nil {
		r = *o.hooks.roots
	} else {
		env := o.Env
		if env == nil {
			env = os.Getenv
		}
		r = defaultDevRoots(env)
	}
	return devtools.Roots{Home: r.Home, Documents: r.Documents, AppData: r.AppData,
		StateFile: filepath.Join(o.Dirs.Run, StateFileName), BinDir: filepath.Join(o.Dirs.Data, BinDir)}
}

// openStore opens the profile store of the mode (R43) with its key.
func openStore(o Options, logf func(string, ...any)) (*store.Store, error) {
	kp, err := keyProvider(o.Mode, o.Dirs, logf)
	if err != nil {
		return nil, fmt.Errorf("daemon: store key: %w", err)
	}
	dir := filepath.Join(o.Dirs.Data, StoreDir, o.Mode.String())
	if o.hooks != nil && o.hooks.storeOpened != nil {
		o.hooks.storeOpened(dir, keyPath(o.Mode, o.Dirs))
	}
	s, err := store.Open(dir, kp)
	if err != nil {
		return nil, fmt.Errorf("daemon: open the store: %w", err)
	}
	return s, nil
}

// keyPath is the key file of the mode's store: the key itself (FileKey) or
// DPAPI-sealed; for the live store on Windows only the source of the R17
// move.
func keyPath(mode safemode.Mode, dirs appdir.Dirs) string {
	return filepath.Join(dirs.Data, StoreDir, mode.String()+".key")
}

// credTarget names the Credential Manager key of one data directory, so two
// data directories (tests, a portable copy) never share or migrate into the
// same credential.
func credTarget(dirs appdir.Dirs) string {
	return "store-" + shortHash(strings.ToLower(filepath.Clean(dirs.Data)))
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// writeAtomic replaces path with data: a temp file in the same directory,
// fsynced, renamed over path.
func writeAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
