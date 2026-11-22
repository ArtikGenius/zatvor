package engine

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/validate"
)

// Phase is where the connection is.
type Phase string

const (
	Disconnected Phase = "disconnected"
	Connecting   Phase = "connecting"
	// Connected: Xray runs, the tunnel check passed and the system proxy
	// points at our inbound.
	Connected Phase = "connected"
	// Degraded: the tunnel broke after a connect (Xray exited or the check
	// failed). The system proxy is reverted so the user's internet works
	// without us; it is applied again once Xray is healthy.
	Degraded Phase = "degraded"
	// Failed: Connect did not finish; nothing of ours is applied.
	Failed Phase = "failed"
)

// Error codes. They are stable strings for the API and the UI.
const (
	CodeCanceled          = "canceled"
	CodeSuperseded        = "superseded" // a newer Connect replaced this one
	CodePortsUnavailable  = "ports_unavailable"
	CodeBuildFailed       = "build_failed"
	CodeGeoPrepareFailed  = "geo_prepare_failed"
	CodeGeoMissingTag     = "geo_missing_tag"
	CodeConfigInvalid     = "config_invalid"
	CodeXrayTestFailed    = "xray_test_failed"
	CodeXrayStartFailed   = "xray_start_failed"
	CodeCheckUnknown      = "tunnel_check_unknown"
	CodeCheckBad          = "tunnel_check_bad"
	CodeProxyConflict     = "proxy_conflict"
	CodeProxyApplyFailed  = "proxy_apply_failed"
	CodeProxyRevertFailed = "proxy_revert_failed"
	CodeXrayExited        = "xray_exited"
	CodeXrayGaveUp        = "xray_gave_up"
	CodeNoConflict        = "no_conflict"
	CodeNotConnected      = "not_connected" // Recheck needs phase Connected
)

// Warning codes (State.Warnings).
const (
	WarnProxyDrift       = "proxy_drift"
	WarnProxyKeptForeign = "proxy_kept_foreign"
	WarnPortsNotSaved    = "ports_not_saved"
	WarnGeoFetchFailed   = "geo_fetch_failed"
	WarnGeoSwitchFailed  = "geo_switch_failed"
	WarnXrayNotInJob     = "xray_not_in_job"
	// CodeProxyConflict and CodeProxyRevertFailed are used as warnings
	// too: a conflict found when re-applying after a recovery, and a
	// failed revert after a crash.
)

// Conflict describes a foreign system proxy we did not overwrite. It names
// the connection ("" is the LAN settings) and whether it is a PAC script;
// the foreign values themselves are never included.
type Conflict struct {
	Conn string `json:"conn"`
	PAC  bool   `json:"pac"`
}

// Error is an engine error or warning: a stable Code and an English
// developer Message. Neither holds secrets (probe credentials, keys,
// subscription URLs) or foreign proxy values.
type Error struct {
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	Conflict *Conflict `json:"conflict,omitempty"`
}

func (e *Error) Error() string { return "engine: " + e.Code + ": " + e.Message }

// newError builds an Error; a cause is appended to msg, with every secret
// value of cfg (when given) scrubbed out.
func newError(code, msg string, cause error, cfg []byte) *Error {
	if cause != nil {
		msg += ": " + cause.Error()
	}
	if cfg != nil {
		msg = validate.Scrub(msg, cfg)
	}
	return &Error{Code: code, Message: msg}
}

// profileError is newError for causes that may quote the profile (a build
// error, a geo download error): every secret value of p is scrubbed, and a
// *url.Error is reduced to its cause so no URL is quoted. If p cannot be
// serialized for scrubbing, the cause is left out.
func profileError(code, msg string, cause error, p profile.Profile) *Error {
	var ue *url.Error
	if errors.As(cause, &ue) && ue.Err != nil {
		cause = ue.Err
	}
	secrets, err := json.Marshal(p)
	if err != nil {
		return &Error{Code: code, Message: msg}
	}
	if pg := profileGeo(p); pg != nil && cause != nil {
		m := cause.Error()
		for _, u := range []string{pg.GeoIPURL, pg.GeoSiteURL} {
			if u != "" {
				m = strings.ReplaceAll(m, u, "<geo url>")
			}
		}
		cause = errors.New(m)
	}
	return newError(code, msg, cause, secrets)
}

func conflictError(code string, ce *sysproxy.ConflictError) *Error {
	return &Error{Code: code, Message: ce.Error(), Conflict: &Conflict{Conn: ce.Conn, PAC: ce.Current.PACURL != ""}}
}

func asConflict(err error) (*sysproxy.ConflictError, bool) {
	var ce *sysproxy.ConflictError
	ok := errors.As(err, &ce)
	return ce, ok
}

// State is a snapshot of the engine. Profile is the profile ID.
type State struct {
	Phase    Phase       `json:"phase"`
	Profile  string      `json:"profile,omitempty"`
	Ports    gen.Ports   `json:"ports"`
	Check    CheckResult `json:"check"`
	Err      *Error      `json:"error,omitempty"`
	Warnings []Error     `json:"warnings,omitempty"`
	// GeoBootstrap: connected on the bundled geo set with everything
	// through the tunnel while the profile's own geo files download
	// (spec §16 Q2.10).
	GeoBootstrap bool `json:"geoBootstrap,omitempty"`
	// Safe: safe mode; system changes go to a recorder.
	Safe bool `json:"safe"`
}

func (s State) clone() State {
	if s.Err != nil {
		e := s.Err.clone()
		s.Err = &e
	}
	if s.Warnings != nil {
		w := make([]Error, len(s.Warnings))
		for i := range s.Warnings {
			w[i] = s.Warnings[i].clone()
		}
		s.Warnings = w
	}
	return s
}

func (e Error) clone() Error {
	if e.Conflict != nil {
		c := *e.Conflict
		e.Conflict = &c
	}
	return e
}

func (s *State) warn(w *Error) {
	s.unwarn(w.Code)
	s.Warnings = append(s.Warnings, w.clone())
}

func (s *State) unwarn(code string) {
	s.Warnings = slices.DeleteFunc(s.Warnings, func(w Error) bool { return w.Code == code })
	if len(s.Warnings) == 0 {
		s.Warnings = nil
	}
}

func (s *State) hasWarning(code string) bool {
	return slices.ContainsFunc(s.Warnings, func(w Error) bool { return w.Code == code })
}

// eventBuf is the Events buffer; when it is full the oldest state is
// dropped, so a slow reader always ends with the latest one.
const eventBuf = 16

// State returns a snapshot of the current state.
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st.clone()
}

// Events delivers a snapshot after every state change. It never blocks
// the engine: when the reader is slow, stale snapshots are dropped and the
// latest is kept.
func (e *Engine) Events() <-chan State { return e.events }

// update changes the state under mu and publishes the result.
func (e *Engine) update(fn func(*State)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(&e.st)
	s := e.st.clone()
	for {
		select {
		case e.events <- s:
			return
		default:
		}
		select { // drop the oldest
		case <-e.events:
		default:
		}
	}
}

func (e *Engine) warn(w *Error) { e.update(func(s *State) { s.warn(w) }) }
