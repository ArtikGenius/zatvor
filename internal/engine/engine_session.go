package engine

import (
	"errors"
	"time"

	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

// loop watches one session: Xray events, the drift poll, re-checks after
// a failed recovery, and the geo bootstrap download.
func (e *Engine) loop(s *session) {
	defer close(s.done)
	drift := e.d.Clock.After(driftInterval)
	var recheck <-chan time.Time
	var fetched chan error
	if s.bootstrap {
		fetched = make(chan error, 1)
		dial := socksDialer(s.in.Ports.Probe, s.in.ProbeUser, s.in.ProbePass)
		go func() { fetched <- e.d.Assets.Fetch(s.ctx, *s.pg, dial) }()
	}
	arm := func(retry bool) {
		if retry {
			recheck = e.d.Clock.After(recheckInterval)
		}
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case ev := <-s.runner.Events():
			arm(e.onRunnerEvent(s, ev))
		case <-drift:
			e.pollDrift(s)
			drift = e.d.Clock.After(driftInterval)
		case <-recheck:
			recheck = nil
			arm(e.onRecheck(s))
		case err := <-fetched:
			fetched = nil
			arm(e.switchGeo(s, err))
		}
	}
}

// lock takes opMu and reports whether s is still the current session.
func (e *Engine) lock(s *session) bool {
	e.opMu.Lock()
	if e.sess != s || s.ctx.Err() != nil {
		e.opMu.Unlock()
		return false
	}
	return true
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
			e.lostXrayLocked(s, newError(CodeXrayExited, "Xray exited", ev.Err, s.cfg))
		}
	case xrayrun.GaveUp:
		e.lostXrayLocked(s, newError(CodeXrayGaveUp, "Xray keeps crashing; no more restarts", ev.Err, s.cfg))
		_ = s.runner.Stop(stopTimeout)
	}
	return false
}

// lostXrayLocked: Xray is down. Revert the system proxy now so the user's
// internet works without us, and go Degraded.
func (e *Engine) lostXrayLocked(s *session, why *Error) {
	e.bumpGen()
	e.setXrayUp(s, false)
	e.degradeLocked(s, why)
}

// degradeLocked reverts our proxy (if applied) and sets phase Degraded.
func (e *Engine) degradeLocked(s *session, why *Error) {
	var warns []*Error
	if s.applied {
		var rerr *Error
		warns, rerr = e.revertLocked()
		if rerr != nil {
			warns = append(warns, rerr)
		}
		s.applied = false
	}
	e.update(func(st *State) {
		st.Phase, st.Err = Degraded, why
		st.unwarn(WarnProxyDrift)
		for _, w := range warns {
			st.warn(w)
		}
	})
}

// recoverLocked: Xray runs again. Wait until it is ready, re-check the
// tunnel and re-apply the system proxy without force. It reports whether a
// re-check should be scheduled.
func (e *Engine) recoverLocked(s *session) bool {
	if err := e.d.Ready(s.in.Ports)(s.ctx); err != nil {
		return s.ctx.Err() == nil
	}
	res := e.check(s.ctx, s.in)
	if s.ctx.Err() != nil {
		return false
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
	if !s.xrayUp || s.applied {
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
// re-check should be scheduled.
func (e *Engine) switchGeo(s *session, fetchErr error) bool {
	if !e.lock(s) {
		return false
	}
	defer e.opMu.Unlock()
	if fetchErr != nil {
		e.warn(newError(WarnGeoFetchFailed, "cannot download the profile's geo files; staying on the bundled set", fetchErr, nil))
		return false
	}
	if !s.xrayUp || !s.applied {
		e.warn(&Error{Code: WarnGeoSwitchFailed, Message: "the tunnel was down when the geo files arrived; staying on the bundled set"})
		return false
	}
	cfg, _, err := gen.Build(s.in)
	if err != nil {
		e.warn(newError(WarnGeoSwitchFailed, "cannot build the profile's config", err, nil))
		return false
	}
	dir, perr := e.prepare(s.ctx, cfg, s.pg)
	if perr != nil {
		if perr.Code != CodeGeoMissingTag {
			perr.Code = WarnGeoSwitchFailed
		}
		e.warn(perr)
		return false
	}

	oldCfg, oldDir := s.cfg, s.dir
	_ = s.runner.Stop(stopTimeout)
	e.bumpGen()
	var cause error
	r := e.d.NewRunner(dir)
	if err := r.Start(s.ctx, cfg, e.d.Ready(s.in.Ports)); err != nil {
		cause = err
	} else if res := e.check(s.ctx, s.in); res.Status != OK {
		_ = r.Stop(stopTimeout)
		cause = errors.New("tunnel check after the switch: " + string(res.Status))
	} else {
		s.runner, s.cfg, s.dir, s.bootstrap = r, cfg, dir, false
		e.update(func(st *State) { st.Check, st.GeoBootstrap = res, false })
		return false
	}
	if s.ctx.Err() != nil {
		return false
	}

	// Roll back to the bootstrap config on its own set.
	r = e.d.NewRunner(oldDir)
	s.runner = r
	if err := r.Start(s.ctx, oldCfg, e.d.Ready(s.in.Ports)); err != nil {
		e.lostXrayLocked(s, newError(CodeXrayStartFailed, "Xray did not start again after a failed geo switch", err, oldCfg))
		return false
	}
	e.warn(newError(WarnGeoSwitchFailed, "the profile's geo set did not work; back on the bundled set", cause, cfg))
	res := e.check(s.ctx, s.in)
	e.update(func(st *State) { st.Check = res })
	if res.Status != OK {
		e.degradeLocked(s, checkError(res))
		return true
	}
	return false
}
