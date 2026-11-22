// Package xrayrun supervises the Xray child process and measures latency
// through the core. It signals only the process it started and never looks
// processes up by name.
package xrayrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ArtikGenius/zatvor/internal/validate"
)

// Clock abstracts time so tests need no real waiting for backoff.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// RealClock is the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time                         { return time.Now() }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// EventKind says what happened to the supervised process.
type EventKind int

const (
	Started    EventKind = iota // process launched
	Exited                      // process ended (Err is set for a crash)
	Restarting                  // a crash restart is scheduled after Delay
	GaveUp                      // too many crashes; no more restarts
)

func (k EventKind) String() string {
	return [...]string{"Started", "Exited", "Restarting", "GaveUp"}[k]
}

// Event is a supervisor notification.
type Event struct {
	Kind  EventKind
	Err   error
	Delay time.Duration // Restarting only
	At    time.Time
}

// ReadyCheck returns nil once Xray accepts connections.
type ReadyCheck func(ctx context.Context) error

const (
	outputCap     = 64 << 10
	errTail       = 2048
	eventBuf      = 64
	crashLimit    = 5
	crashWindow   = 2 * time.Minute
	readyTimeout  = 10 * time.Second
	readyInterval = 50 * time.Millisecond
)

var backoff = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// DialReady dials addr until it accepts, for at most 10 seconds.
func DialReady(addr string) ReadyCheck {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, readyTimeout)
		defer cancel()
		var d net.Dialer
		for {
			c, err := d.DialContext(ctx, "tcp", addr)
			if err == nil {
				return c.Close()
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("xray not ready at %s: %w", addr, err)
			case <-time.After(readyInterval):
			}
		}
	}
}

// Runner starts Xray with the config on stdin and restarts it after crashes.
type Runner struct {
	xrayPath, assetDir string
	clock              Clock
	events             chan Event

	mu       sync.Mutex
	running  bool // Start succeeded and Stop has not completed
	stopping bool
	cur      *os.Process
	stopCh   chan struct{}
	done     chan struct{} // closed when the supervisor loop has ended
	firstEnd chan struct{} // closed on the first process exit
	exitErr  error         // first crash error
}

// New creates a Runner. A nil clock means the wall clock.
func New(xrayPath, assetDir string, clock Clock) *Runner {
	if clock == nil {
		clock = RealClock{}
	}
	return &Runner{xrayPath: xrayPath, assetDir: assetDir, clock: clock, events: make(chan Event, eventBuf)}
}

// Events delivers supervisor events. The channel is buffered; when the
// reader is slow the oldest event is dropped so the supervisor never blocks.
func (r *Runner) Events() <-chan Event { return r.events }

func (r *Runner) emit(ev Event) {
	ev.At = r.clock.Now()
	for {
		select {
		case r.events <- ev:
			return
		default:
		}
		select { // drop the oldest
		case <-r.events:
		default:
		}
	}
}

// Pid is the process id of the running child, or 0.
func (r *Runner) Pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return 0
	}
	return r.cur.Pid
}

// Start launches Xray with cfg on stdin (never on disk). cfg is kept in
// memory for restarts only. With a ready check, Start returns once it
// passes; if it fails, or Xray exits first, the child is stopped and the
// error is returned. ctx bounds the readiness wait only; the child lives
// until Stop.
func (r *Runner) Start(ctx context.Context, cfg []byte, ready ReadyCheck) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("xrayrun: already started")
	}
	r.running, r.stopping = true, false
	r.stopCh, r.done, r.firstEnd = make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.mu.Unlock()

	cfg = append([]byte(nil), cfg...)
	w, err := r.spawn(cfg)
	if err != nil {
		r.mu.Lock()
		r.running = false
		close(r.done) // release a concurrent Stop that already saw running
		r.mu.Unlock()
		return err
	}
	go r.loop(cfg, w)

	if ready == nil {
		return nil
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- ready(rctx) }()
	var failure error
	select {
	case err := <-res:
		if err == nil {
			return nil
		}
		failure = fmt.Errorf("xray not ready: %w", err)
	case <-r.firstEnd:
		failure = fmt.Errorf("xray exited before it was ready: %w", r.lastExitErr())
	}
	cancel()
	_ = r.Stop(2 * time.Second)
	return failure
}

func (r *Runner) lastExitErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitErr
}

type waiter struct {
	cmd *exec.Cmd
	out *ringBuf
	end chan error
}

func (r *Runner) spawn(cfg []byte) (*waiter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping {
		return nil, errors.New("xrayrun: stopping")
	}
	cmd := exec.Command(r.xrayPath, "run", "-config", "stdin:")
	cmd.Env = validate.MinimalEnv(r.assetDir)
	cmd.Dir = r.assetDir
	if cmd.Dir == "" {
		cmd.Dir = os.TempDir()
	}
	cmd.Stdin = bytes.NewReader(cfg)
	out := newRingBuf(outputCap)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start xray: %s", validate.Scrub(err.Error(), cfg))
	}
	r.cur = cmd.Process
	w := &waiter{cmd: cmd, out: out, end: make(chan error, 1)}
	go func() { w.end <- cmd.Wait() }()
	r.emit(Event{Kind: Started})
	return w, nil
}

func (r *Runner) loop(cfg []byte, w *waiter) {
	defer close(r.done)
	var crashes []time.Time
	first := true
	for {
		err := <-w.end
		r.mu.Lock()
		r.cur = nil
		stopping := r.stopping
		r.mu.Unlock()
		if stopping {
			r.emit(Event{Kind: Exited})
			return
		}
		if err == nil {
			err = errors.New("exited unexpectedly")
		}
		crashErr := r.crashError(err, w.out, cfg)
		if first {
			first = false
			r.mu.Lock()
			r.exitErr = crashErr
			r.mu.Unlock()
			close(r.firstEnd)
		}
		r.emit(Event{Kind: Exited, Err: crashErr})
		now := r.clock.Now()
		crashes = append(crashes, now)
		for len(crashes) > 0 && now.Sub(crashes[0]) >= crashWindow {
			crashes = crashes[1:]
		}
		if len(crashes) >= crashLimit {
			r.emit(Event{Kind: GaveUp, Err: fmt.Errorf("xray crashed %d times within %v: %w", len(crashes), crashWindow, crashErr)})
			return
		}
		delay := backoff[min(len(crashes), len(backoff))-1]
		timer := r.clock.After(delay)
		r.emit(Event{Kind: Restarting, Delay: delay})
		select {
		case <-r.stopCh:
			return
		case <-timer:
		}
		for {
			nw, serr := r.spawn(cfg)
			if serr == nil {
				w = nw
				break
			}
			if r.isStopping() {
				return
			}
			// A failed launch counts as a crash too.
			now = r.clock.Now()
			crashes = append(crashes, now)
			for len(crashes) > 0 && now.Sub(crashes[0]) >= crashWindow {
				crashes = crashes[1:]
			}
			r.emit(Event{Kind: Exited, Err: serr})
			if len(crashes) >= crashLimit {
				r.emit(Event{Kind: GaveUp, Err: serr})
				return
			}
			delay = backoff[min(len(crashes), len(backoff))-1]
			timer = r.clock.After(delay)
			r.emit(Event{Kind: Restarting, Delay: delay})
			select {
			case <-r.stopCh:
				return
			case <-timer:
			}
		}
	}
}

func (r *Runner) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping
}

func (r *Runner) crashError(err error, out *ringBuf, cfg []byte) error {
	tail := strings.TrimSpace(validate.Scrub(out.String(), cfg))
	if len(tail) > errTail {
		tail = "..." + tail[len(tail)-errTail:]
	}
	if tail == "" {
		return fmt.Errorf("xray exited: %v", err)
	}
	return fmt.Errorf("xray exited: %v: %s", err, tail)
}

// Stop ends the supervised child: a graceful interrupt first (Unix), then
// Kill after timeout; on Windows it kills at once. Only the process this
// Runner started is signalled. Stopping a runner that is not running is a
// no-op.
func (r *Runner) Stop(timeout time.Duration) error {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return nil
	}
	if !r.stopping {
		r.stopping = true
		close(r.stopCh)
	}
	p, done := r.cur, r.done
	r.mu.Unlock()

	var killErr error
	if p != nil {
		if runtime.GOOS == "windows" {
			killErr = ignoreDone(p.Kill())
		} else if err := p.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			killErr = ignoreDone(p.Kill())
		}
		select {
		case <-done:
		case <-time.After(timeout):
			killErr = ignoreDone(p.Kill())
			<-done
		}
	} else {
		<-done
	}
	r.mu.Lock()
	r.running, r.cur = false, nil
	r.mu.Unlock()
	return killErr
}

func ignoreDone(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// ringBuf keeps the last cap bytes written to it.
type ringBuf struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newRingBuf(capacity int) *ringBuf { return &ringBuf{cap: capacity} }

func (b *ringBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.cap {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.cap:]...)
	}
	return len(p), nil
}

func (b *ringBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
