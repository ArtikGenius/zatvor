// Package supervisor runs the daemon as a child process and restarts it
// after crashes (spec §8). When the daemon cannot come back it reverts the
// change journal itself, so the user's system proxy is restored even then.
//
// The journal has no file lock (R10): the supervisor touches it only while
// no child runs, that is after the child's Wait has returned.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

const (
	// CrashLimit crashes within CrashWindow make the supervisor give up.
	CrashLimit = 5
	// CrashWindow is the sliding window crashes are counted in. A child
	// that ran at least this long also starts the backoff from the first
	// step again.
	CrashWindow = 2 * time.Minute
)

// backoff is the delay before the n-th restart in a row; the last step
// repeats.
var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// stopTimeout is how long a child may take to exit after its stdin was
// closed before it is killed.
var stopTimeout = 30 * time.Second

// afterStart is a test hook run right after a child started.
var afterStart func()

// ErrGaveUp: the daemon crashed CrashLimit times within CrashWindow.
var ErrGaveUp = errors.New("supervisor: the daemon keeps crashing")

// GaveUpError is the error of a give-up. It wraps ErrGaveUp and counts the
// results of the journal revert that followed.
type GaveUpError struct {
	Crashes                   int
	Restored, Skipped, Failed int
	// Locked: another instance held the lock, so the journal was not ours
	// to revert and nothing was reverted.
	Locked bool
}

func (e *GaveUpError) Error() string {
	if e.Locked {
		return fmt.Sprintf("%v (%d crashes; another instance runs, nothing reverted)", ErrGaveUp, e.Crashes)
	}
	return fmt.Sprintf("%v (%d crashes; revert: %d restored, %d skipped, %d failed)",
		ErrGaveUp, e.Crashes, e.Restored, e.Skipped, e.Failed)
}

func (e *GaveUpError) Unwrap() error { return ErrGaveUp }

// Option configures Run.
type Option func(*config)

type config struct {
	lock        func() (release func(), ok bool)
	logf        func(format string, args ...any)
	afterRevert func() error
}

// WithAfterRevert runs f after each revert of the supervisor's own, still
// under the revert lock; the daemon's empties the dev-tool state file
// (daemon.Reverters). It is not run when the lock was refused.
func WithAfterRevert(f func() error) Option {
	return func(c *config) { c.afterRevert = f }
}

// WithRevertLock makes every revert take lock first. When ok is false
// another daemon instance owns the journal and nothing is reverted.
func WithRevertLock(lock func() (release func(), ok bool)) Option {
	return func(c *config) { c.lock = lock }
}

// WithLogf sets the logger. Nothing secret is passed to it.
func WithLogf(logf func(format string, args ...any)) Option {
	return func(c *config) { c.logf = logf }
}

// Run starts child() and supervises it until it exits with code 0 (Run
// returns nil) or ctx ends. A crash (any other exit, or a failed start) is
// restarted after 1, 2, 4, 8, 16, 16, ... s; after CrashLimit crashes
// within CrashWindow, Run reverts j with rs and returns a *GaveUpError.
//
// When ctx ends, the child's stdin is closed, which asks it to shut down
// cleanly (the daemon then reverts on its own), and Run waits for it; a
// child that does not exit within 30 s is killed. If the child did not exit
// cleanly, or had crashed already, Run reverts j itself before it returns.
// child must not set Stdin. A nil clock is the wall clock.
func Run(ctx context.Context, child func() *exec.Cmd, j *journal.Journal, rs map[string]journal.Reverter, clock xrayrun.Clock, opts ...Option) error {
	if clock == nil {
		clock = xrayrun.RealClock{}
	}
	c := config{logf: func(string, ...any) {}}
	for _, o := range opts {
		o(&c)
	}
	var crashes []time.Time
	inRow := 0
	for {
		if ctx.Err() != nil {
			if len(crashes) > 0 {
				// The last child crashed and did not revert.
				c.revert(j, rs)
			}
			return nil
		}
		started := clock.Now()
		clean, stopped := c.runOnce(ctx, child)
		if clean {
			return nil
		}
		if stopped {
			// Asked to stop, but the child was killed or failed on its
			// way out: it did not revert.
			c.revert(j, rs)
			return nil
		}
		now := clock.Now()
		if now.Sub(started) >= CrashWindow {
			inRow = 0
		}
		inRow++
		crashes = append(crashes, now)
		for len(crashes) > 0 && now.Sub(crashes[0]) > CrashWindow {
			crashes = crashes[1:]
		}
		if len(crashes) >= CrashLimit {
			c.logf("supervisor: %d crashes within %s, giving up", len(crashes), CrashWindow)
			gu := &GaveUpError{Crashes: len(crashes)}
			gu.Restored, gu.Skipped, gu.Failed, gu.Locked = c.revert(j, rs)
			return gu
		}
		delay := backoff[min(inRow, len(backoff))-1]
		c.logf("supervisor: the daemon crashed, restart in %s", delay)
		select {
		case <-ctx.Done():
		case <-clock.After(delay):
		}
	}
}

// runOnce starts one child and waits for it. clean is true when it exited
// with code 0 (and was not killed); stopped is true when ctx ended while it
// ran.
func (c *config) runOnce(ctx context.Context, child func() *exec.Cmd) (clean, stopped bool) {
	cmd := child()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.logf("supervisor: cannot prepare the daemon's stdin")
		return false, false
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		c.logf("supervisor: cannot start the daemon")
		return false, false
	}
	if afterStart != nil {
		afterStart()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil, false
	case <-ctx.Done():
	}
	stdin.Close()
	timer := time.NewTimer(stopTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err == nil, true
	case <-timer.C:
	}
	c.logf("supervisor: the daemon did not stop within %s, killing it", stopTimeout)
	_ = cmd.Process.Kill()
	<-done
	return false, true
}

// revert runs RevertAll; the caller guarantees that no child is alive.
func (c *config) revert(j *journal.Journal, rs map[string]journal.Reverter) (restored, skipped, failed int, locked bool) {
	if c.lock != nil {
		release, ok := c.lock()
		if !ok {
			c.logf("supervisor: another instance runs; not reverting its journal")
			return 0, 0, 0, true
		}
		defer release()
	}
	for _, r := range j.RevertAll(rs) {
		switch r.Outcome {
		case journal.Restored:
			restored++
		case journal.SkippedChangedByOther:
			skipped++
		default:
			failed++
		}
		c.logf("supervisor: revert %s: %s", r.Kind, r.Outcome)
	}
	if c.afterRevert != nil {
		if err := c.afterRevert(); err != nil {
			c.logf("supervisor: the cleanup after the revert failed")
		}
	}
	return restored, skipped, failed, false
}
