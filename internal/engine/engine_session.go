package engine

import (
	"context"
	"errors"
	"time"

	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

// loop watches one session: Xray events, the drift poll, re-checks after
// a failed recovery (with a growing interval), and the geo bootstrap
// download.
func (e *Engine) loop(s *session) {
	defer close(s.done)
	drift := e.d.Clock.After(driftInterval)
	var recheck, switchAgain <-chan time.Time
	var fetched chan error
	if s.bootstrap {
		fetched = make(chan error, 1)
		dial := socksDialer(s.in.Ports.Probe, s.in.ProbeUser, s.in.ProbePass)
		go func() { fetched <- e.d.Assets.Fetch(s.ctx, *s.pg, dial) }()
	}
	// The re-check backs off 10 s, 20 s, 40 s... up to recheckMax while
	// recovery keeps failing; once the session is Connected again the next
	// failure starts at 10 s. A new connect starts a new loop. A failed
	// revert is retried every 10 s: until it succeeds the system proxy
	// points at a dead port.
	delay := recheckInterval
	arm := func(retry bool) {
		revertPending, connected := e.recheckState(s)
		switch {
		case retry && revertPending:
			recheck = e.d.Clock.After(recheckInterval)
		case retry:
			recheck = e.d.Clock.After(delay)
			delay = min(2*delay, recheckMax)
		case connected:
			delay = recheckInterval
		}
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev := <-s.events: // nil (blocks) while no runner is adopted
			arm(e.onRunnerEvent(s, ev))
		case <-drift:
			e.pollDrift(s)
			drift = e.d.Clock.After(driftInterval)
		case <-recheck:
			recheck = nil
			arm(e.onRecheck(s))
		case err := <-fetched:
			fetched = nil
			retry, again := e.switchGeo(s, err)
			arm(retry)
			if again { // interrupted by a connect: try again shortly
				switchAgain = e.d.Clock.After(switchRetry)
			}
		case <-switchAgain:
			switchAgain = nil
			retry, again := e.switchGeo(s, nil)
			arm(retry)
			if again {
				switchAgain = e.d.Clock.After(switchRetry)
			}
		}
	}
}

// recheckState reports whether a revert of s is pending, and whether s is
// the current session in phase Connected.
func (e *Engine) recheckState(s *session) (revertPending, connected bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return s.revertPending, e.sess == s && e.st.Phase == Connected
}

func (e *Engine) setRevertPending(s *session, v bool) {
	e.mu.Lock()
	s.revertPending = v
	e.mu.Unlock()
}

// lock takes opMu and reports whether s is still the current session. It
// gives the handler a fresh work context when the last one was
// interrupted, unless a connect is still waiting for opMu: then the work
// stays cancelled, so slow steps return at once and let that connect in.
func (e *Engine) lock(s *session) bool {
	e.opMu.Lock()
	if e.sess != s || s.ctx.Err() != nil {
		e.opMu.Unlock()
		return false
	}
	e.mu.Lock()
	if s.work.Err() != nil && e.pendingConnects == 0 {
		s.work, s.workCancel = context.WithCancel(s.ctx)
	}
	e.mu.Unlock()
	return true
}

// interruptWork cancels the slow step a handler is in. Call with mu held.
func (s *session) interruptWork() {
	s.workCancel()
}

// onRunnerEvent applies the fail policy. It reports whether a re-check
// should be scheduled.
func (e *Engine) onRunnerEvent(s *session, ev xrayrun.Event) bool {
	if !e.lock(s) {
		return false
	}
	defer e.opMu.Unlock()
	switch ev.Kind {
	case xrayrun.Started:
		if ev.Err != nil {
			e.warn(&Error{Code: WarnXrayNotInJob, Message: "Xray may outlive the daemon: " + ev.Err.Error()})
		}
		if s.xrayUp {
			return false // the launch Connect (or a switch) already waited for
		}
		e.setXrayUp(s, true) // restarted after a crash
		return e.recoverLocked(s)
	case xrayrun.Exited:
		if ev.Err != nil { // nil: our own Stop
			return e.lostXrayLocked(s, newError(CodeXrayExited, "Xray exited", ev.Err, s.cfg))
		}
	case xrayrun.GaveUp:
		retry := e.lostXrayLocked(s, newError(CodeXrayGaveUp, "Xray keeps crashing; no more restarts", ev.Err, s.cfg))
		_ = s.runner.Stop(stopTimeout)
		return retry
	}
	return false
}

// adopt makes r, whose Start succeeded, the session's runner: the loop
// reads its events from now on. A runner whose Start failed is never
// adopted, so its queued events (Started, then Exited from its own stop)
// can never be taken for a recovery.
func (s *session) adopt(r Runner) {
	s.runner, s.events = r, r.Events()
}

// lostXrayLocked: Xray is down. Revert the system proxy now so the user's
// internet works without us, and go Degraded. It reports whether the
// revert failed and must be retried.
func (e *Engine) lostXrayLocked(s *session, why *Error) bool {
	e.bumpGen()
	e.setXrayUp(s, false)
	return e.degradeLocked(s, why)
}

// degradeLocked reverts our proxy (if applied, or if an earlier revert
// failed) and sets phase Degraded. It reports whether the revert failed:
// the caller schedules a re-check, which retries it until it succeeds, so
// the system proxy is never left pointing at a dead port (Review Focus 5).
func (e *Engine) degradeLocked(s *session, why *Error) bool {
	var warns []*Error
	if s.applied || s.revertPending {
		warns = e.retryRevertLocked(s)
	}
	e.update(func(st *State) {
		st.Phase, st.Err = Degraded, why
		st.unwarn(WarnProxyDrift)
		for _, w := range warns {
			st.warn(w)
		}
	})
	return s.revertPending
}

// retryRevertLocked reverts and records the outcome in s.revertPending. It
// returns the warnings to show; a successful revert clears the
// proxy_revert_failed warning.
func (e *Engine) retryRevertLocked(s *session) []*Error {
	warns, rerr := e.revertLocked()
	s.applied = false
	e.setRevertPending(s, rerr != nil)
	if rerr != nil {
		return append(warns, rerr)
	}
	e.update(func(st *State) { st.unwarn(CodeProxyRevertFailed) })
	return warns
}

// recoverLocked: Xray runs again. Wait until it is ready, re-check the
// tunnel and re-apply the system proxy without force. It reports whether a
// re-check should be scheduled.
func (e *Engine) recoverLocked(s *session) bool {
	// An interrupted step changes nothing and is retried on the re-check
	// timer.
	if s.work.Err() != nil {
		return true
	}
	if err := e.d.Ready(s.in)(s.work); err != nil {
		return true
	}
	res := e.check(s.work, s.in)
	if s.work.Err() != nil {
		return true
	}
	e.update(func(st *State) { st.Check = res })
	if res.Status != OK {
		e.degradeLocked(s, checkError(res))
		return true
	}
	if err := e.d.Proxy.Apply(sysproxy.Ours(s.in.Ports.Mixed), false); err != nil {
		if ce, ok := asConflict(err); ok {
			e.conflict = &s.prof
			e.update(func(st *State) { st.Err = nil; st.warn(conflictError(CodeProxyConflict, ce)) })
			return false // the user decides (ResolveConflict)
		}
		s.applied = true // Apply may have set some connections
		e.degradeLocked(s, newError(CodeProxyApplyFailed, "cannot set the system proxy", err, nil))
		return true
	}
	s.applied = true
	e.setRevertPending(s, false)
	e.update(func(st *State) {
		st.Phase, st.Err = Connected, nil
		st.unwarn(CodeProxyConflict)
		st.unwarn(CodeProxyRevertFailed)
	})
	return false
}

func (e *Engine) onRecheck(s *session) bool {
	if !e.lock(s) {
		return false
	}
	defer e.opMu.Unlock()
	if s.revertPending {
		warns := e.retryRevertLocked(s)
		for _, w := range warns {
			e.warn(w)
		}
		if s.revertPending {
			return true
		}
	}
	if !s.xrayUp || s.applied || e.conflict != nil {
		return false
	}
	return e.recoverLocked(s)
}

// pollDrift warns when someone else changed the system proxy while we are
// connected. It never applies ours again over theirs.
func (e *Engine) pollDrift(s *session) {
	if !e.lock(s) {
		return
	}
	defer e.opMu.Unlock()
	if !s.applied {
		return
	}
	drifted, _, err := e.d.Proxy.Drifted()
	if err != nil {
		return
	}
	e.mu.Lock()
	has := e.st.hasWarning(WarnProxyDrift)
	e.mu.Unlock()
	switch {
	case drifted && !has:
		e.warn(&Error{Code: WarnProxyDrift,
			Message: "another program changed the system proxy; it is left as set and will not be restored over"})
	case !drifted && has:
		e.update(func(st *State) { st.unwarn(WarnProxyDrift) })
	}
}

// switchGeo ends the geo bootstrap: after the profile's geo files are
// fetched, rebuild without the bootstrap rule and restart Xray on the new
// set. If the new run fails to start (or its check fails), the bootstrap
// config is restarted on its own set with a warning (spec §6.5 rollback).
// The system proxy stays applied throughout. It reports whether a
// re-check should be scheduled, and whether the switch was interrupted
// before it changed anything and should run again.
func (e *Engine) switchGeo(s *session, fetchErr error) (retry, again bool) {
	if !e.lock(s) {
		return false, false
	}
	defer e.opMu.Unlock()
	if fetchErr != nil {
		e.warn(profileError(WarnGeoFetchFailed, "cannot download the profile's geo files; staying on the bundled set", fetchErr, s.prof))
		return false, false
	}
	if !s.xrayUp || !s.applied {
		e.warn(&Error{Code: WarnGeoSwitchFailed, Message: "the tunnel was down when the geo files arrived; staying on the bundled set"})
		return false, false
	}
	cfg, _, err := gen.Build(s.in)
	if err != nil {
		e.warn(profileError(WarnGeoSwitchFailed, "cannot build the profile's config", err, s.prof))
		return false, false
	}
	if s.work.Err() != nil {
		return false, true
	}
	dir, perr := e.prepare(s.work, cfg, s.pg)
	if perr != nil && s.work.Err() != nil {
		return false, true
	}
	if perr != nil {
		if perr.Code != CodeGeoMissingTag {
			perr.Code = WarnGeoSwitchFailed
		}
		e.warn(perr)
		return false, false
	}

	oldCfg, oldDir := s.cfg, s.dir
	_ = s.runner.Stop(stopTimeout)
	e.bumpGen()
	var cause error
	r := e.newRunner(dir)
	if err := r.Start(s.ctx, cfg, e.d.Ready(s.in)); err != nil {
		cause = err
	} else if res := e.check(s.ctx, s.in); res.Status != OK {
		_ = r.Stop(stopTimeout)
		cause = errors.New("tunnel check after the switch: " + string(res.Status))
	} else {
		s.adopt(r)
		s.cfg, s.dir, s.bootstrap = cfg, dir, false
		e.update(func(st *State) { st.Check, st.GeoBootstrap = res, false })
		return false, false
	}
	if s.ctx.Err() != nil {
		return false, false
	}

	// Roll back to the bootstrap config on its own set.
	r = e.newRunner(oldDir)
	if err := r.Start(s.ctx, oldCfg, e.d.Ready(s.in)); err != nil {
		s.events = nil // no runner left to watch; the stopped one stays for teardown
		return e.lostXrayLocked(s, newError(CodeXrayStartFailed, "Xray did not start again after a failed geo switch", err, oldCfg)), false
	}
	s.adopt(r)
	e.warn(newError(WarnGeoSwitchFailed, "the profile's geo set did not work; back on the bundled set", cause, cfg))
	res := e.check(s.ctx, s.in)
	e.update(func(st *State) { st.Check = res })
	if res.Status != OK {
		e.degradeLocked(s, checkError(res))
		return true, false
	}
	return false, false
}
