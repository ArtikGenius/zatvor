// Package subsvc adds, updates, lists and removes subscriptions. It owns the
// request policy of spec 5.6 and R3.6: one user action is one request, scheduled
// updates are sent only through the tunnel, and a failed update never touches
// the stored subscription.
package subsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/ArtikGenius/zatvor/internal/gen"
	"github.com/ArtikGenius/zatvor/internal/hwid"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
	"github.com/ArtikGenius/zatvor/internal/validate"
	"github.com/ArtikGenius/zatvor/internal/xrayrun"
)

// Error codes. Messages never contain the subscription URL, the HWID or the
// machine ID.
const (
	CodeNoTunnel         = "no_tunnel"
	CodeDeviceLimit      = "device_limit"
	CodeHWIDRequired     = "hwid_required"
	CodeEncrypted        = "encrypted"
	CodeClashUnsupported = "clash_unsupported"
	CodeNotSubscription  = "not_subscription"
	CodeEmpty            = "empty"
	CodeNetwork          = "network"
	CodeHTTPStatus       = "http_status"
	CodeInvalidProfiles  = "invalid_profiles"
	CodeNotFound         = "not_found"
	CodeInvalidURL       = "invalid_url"
	CodeStore            = "store"
)

// Error is the typed error of every Service method.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// Trigger says why an update runs.
type Trigger int

const (
	// User: the user pressed the button. One request, through the tunnel if
	// it is up, else direct.
	User Trigger = iota
	// Scheduled: the scheduler. Sent only through the tunnel.
	Scheduled
	// Start: the daemon's start-up refresh. One request, tunnel if up else
	// direct, never retried.
	Start
)

// Fetcher is *subfetch.Fetcher; tests count requests through it.
type Fetcher interface {
	Fetch(ctx context.Context, req subfetch.Request) (subfetch.Result, error)
}

// Store is *store.Store.
type Store interface {
	Load() ([]profile.Subscription, error)
	Save([]profile.Subscription) error
}

// Tunnel is *engine.Engine: the probe dialer exists only while connected.
type Tunnel interface {
	ProbeDialer() (subfetch.Dialer, bool)
}

// Deps are the Service's collaborators.
type Deps struct {
	Fetcher Fetcher
	Store   Store
	// Tunnel must be a nil interface, not a typed nil pointer, when there is
	// no engine.
	Tunnel    Tunnel // nil: never connected
	MachineID string
	Device    hwid.Device
	Clock     xrayrun.Clock // nil: wall clock
	Version   string
	OS        string
	// BuildCheck must accept a profile for it to be saved. Nil: gen.Build
	// with placeholder ports plus validate.Schema.
	BuildCheck func(profile.Profile) error
	Logf       func(format string, args ...any) // optional; no secrets are passed
}

// Service is safe for concurrent use.
type Service struct {
	d      Deps
	mu     sync.Mutex // guards the store read-modify-write and sched
	flight singleflight.Group
	sched  map[string]*backoff
	events *eventBus
}

// New returns a Service.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = xrayrun.RealClock{}
	}
	if d.BuildCheck == nil {
		d.BuildCheck = defaultBuildCheck
	}
	return &Service{d: d, sched: map[string]*backoff{}, events: newEventBus()}
}

func defaultBuildCheck(p profile.Profile) error {
	cfg, _, err := gen.Build(gen.Input{
		Profile:   p,
		Ports:     gen.Ports{Mixed: 20001, Probe: 20002, DevTools: 20003, API: 20004},
		ProbeUser: "check", ProbePass: "check",
	})
	if err != nil {
		return err
	}
	_, err = validate.Schema(cfg)
	return err
}

func (s *Service) logf(format string, args ...any) {
	if s.d.Logf != nil {
		s.d.Logf(format, args...)
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func parseURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}
	return u, true
}

// Add fetches rawURL (one direct request, or through the tunnel if it is up)
// and stores the new subscription with SendDevice on. Nothing is saved unless
// the fetch yields a valid profile. An empty ua means the default UA.
func (s *Service) Add(ctx context.Context, rawURL, ua string) (profile.Subscription, profile.Diff, error) {
	if _, ok := parseURL(rawURL); !ok {
		return profile.Subscription{}, profile.Diff{}, fail(CodeInvalidURL, "subscription URL is not a valid http(s) URL")
	}
	if ua == "" {
		ua = subfetch.DefaultUA(s.d.Version, s.d.OS)
	}
	sub := profile.Subscription{ID: newID(), URL: rawURL, UA: ua, SendDevice: true}
	next, diff, err := s.fetchApply(ctx, sub, User)
	if err != nil {
		s.events.publish(Event{Kind: Failed, Code: codeOf(err)})
		return profile.Subscription{}, profile.Diff{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, lerr := s.d.Store.Load()
	if lerr != nil {
		return profile.Subscription{}, profile.Diff{}, fail(CodeStore, "cannot read the profile store")
	}
	if serr := s.d.Store.Save(append(subs, next)); serr != nil {
		return profile.Subscription{}, profile.Diff{}, fail(CodeStore, "cannot save the profile store")
	}
	s.events.publish(Event{Kind: Updated, ID: next.ID, Diff: diff})
	return redacted(next), diff, nil
}

// fetchTimeout bounds a shared flight, which no single caller's cancel may kill.
const fetchTimeout = 30 * time.Second

type flightResult struct {
	diff profile.Diff
	err  error
}

// Update refreshes one subscription. Concurrent calls of the same kind
// (scheduled, or user/start) for the same id share one request; a button
// press never joins a scheduled flight. On any failure the stored
// subscription is left untouched.
func (s *Service) Update(ctx context.Context, id string, trig Trigger) (profile.Diff, error) {
	key := id + "/user"
	if trig == Scheduled {
		key = id + "/scheduled"
	}
	ch := s.flight.DoChan(key, func() (any, error) {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()
		d, err := s.update(fctx, id, trig)
		return flightResult{d, err}, nil
	})
	select {
	case <-ctx.Done():
		return profile.Diff{}, fail(CodeNetwork, "the update was canceled")
	case out := <-ch:
		r := out.Val.(flightResult)
		return r.diff, r.err
	}
}

func (s *Service) update(ctx context.Context, id string, trig Trigger) (profile.Diff, error) {
	s.mu.Lock()
	subs, err := s.d.Store.Load()
	s.mu.Unlock()
	if err != nil {
		return profile.Diff{}, fail(CodeStore, "cannot read the profile store")
	}
	var old *profile.Subscription
	for i := range subs {
		if subs[i].ID == id {
			old = &subs[i]
		}
	}
	if old == nil {
		return profile.Diff{}, fail(CodeNotFound, "no such subscription")
	}
	if trig == Start {
		// Record the attempt: the scheduler must not fire a second request
		// right after it (a failed Start is not retried before the interval).
		s.mu.Lock()
		s.sched[id] = &backoff{next: s.d.Clock.Now().Add(interval(old.Info.UpdateInterval))}
		s.mu.Unlock()
	}
	next, diff, ferr := s.fetchApply(ctx, *old, trig)
	if ferr != nil {
		s.events.publish(Event{Kind: Failed, ID: id, Code: codeOf(ferr)})
		return profile.Diff{}, ferr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err = s.d.Store.Load()
	if err != nil {
		return profile.Diff{}, fail(CodeStore, "cannot read the profile store")
	}
	found := false
	for i := range subs {
		if subs[i].ID != id {
			continue
		}
		found = true
		// Only the fetched fields: a SetUA made during the fetch survives.
		cur := &subs[i]
		cur.Profiles = next.Profiles
		cur.Info = next.Info
		cur.Title = next.Title
		cur.ProviderRouting = next.ProviderRouting
		cur.LastGood = next.LastGood
	}
	if !found {
		return profile.Diff{}, fail(CodeNotFound, "no such subscription")
	}
	if err := s.d.Store.Save(subs); err != nil {
		return profile.Diff{}, fail(CodeStore, "cannot save the profile store")
	}
	delete(s.sched, id)
	s.events.publish(Event{Kind: Updated, ID: id, Diff: diff})
	return diff, nil
}

func codeOf(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}

var problemCodes = map[subfetch.Problem][2]string{
	subfetch.Network:          {CodeNetwork, "could not reach the subscription server"},
	subfetch.HTTPStatus:       {CodeHTTPStatus, "the subscription server answered with an error status"},
	subfetch.EmptyBody:        {CodeEmpty, "the subscription is empty"},
	subfetch.NotASubscription: {CodeNotSubscription, "the answer is not a subscription"},
	subfetch.DeviceLimit:      {CodeDeviceLimit, "the provider reports that the device limit is reached"},
	subfetch.HWIDRequired:     {CodeHWIDRequired, "the provider requires a device identifier"},
	subfetch.Encrypted:        {CodeEncrypted, "the subscription is encrypted and cannot be read"},
	subfetch.ClashUnsupported: {CodeClashUnsupported, "Clash subscriptions are not supported"},
}

// fetchApply makes the one request and merges the result into old. It never
// writes to the store.
func (s *Service) fetchApply(ctx context.Context, old profile.Subscription, trig Trigger) (profile.Subscription, profile.Diff, error) {
	u, ok := parseURL(old.URL)
	if !ok {
		return old, profile.Diff{}, fail(CodeInvalidURL, "subscription URL is not a valid http(s) URL")
	}
	var via subfetch.Dialer
	if s.d.Tunnel != nil {
		if dl, up := s.d.Tunnel.ProbeDialer(); up {
			via = dl
		}
	}
	if trig == Scheduled && via == nil {
		return old, profile.Diff{}, fail(CodeNoTunnel, "the tunnel is not connected")
	}
	ua := old.UA
	if ua == "" {
		ua = subfetch.DefaultUA(s.d.Version, s.d.OS)
	}
	plain := u.Scheme == "http"
	req := subfetch.Request{URL: old.URL, UA: ua, Via: via}
	if !plain {
		req.HWID = hwid.Headers(s.d.MachineID, u.Host, s.d.Device, hwid.Options{SendDevice: old.SendDevice})
	}
	res, err := s.d.Fetcher.Fetch(ctx, req)
	if err != nil {
		// The fetcher's error text names the host only, but a fixed message
		// is the safe contract.
		return old, profile.Diff{}, fail(CodeNetwork, "could not reach the subscription server")
	}
	if pc, bad := problemCodes[res.Problem]; bad {
		return old, profile.Diff{}, fail(pc[0], pc[1])
	}
	next, diff, aerr := profile.Apply(old, res, s.d.Clock.Now())
	if aerr != nil {
		return old, profile.Diff{}, fail(CodeEmpty, "the subscription has no usable profiles")
	}
	next, diff, aerr = s.dropInvalid(old, next, diff)
	if aerr != nil {
		return old, profile.Diff{}, aerr
	}
	if plain {
		diff.Warnings = append(diff.Warnings, "plain_http")
	}
	return next, diff, nil
}

// dropInvalid handles profiles that fail BuildCheck. A new one is dropped
// (warning profile_invalid); one that already worked keeps its old version
// (warning profile_kept_old). Warnings carry the title only.
func (s *Service) dropInvalid(old, next profile.Subscription, diff profile.Diff) (profile.Subscription, profile.Diff, error) {
	oldByID := map[string]profile.Profile{}
	for _, p := range old.Profiles {
		oldByID[p.ID] = p
	}
	var keep []profile.Profile
	dropped := map[string]bool{}
	for _, p := range next.Profiles {
		if err := s.d.BuildCheck(p); err == nil {
			keep = append(keep, p)
			continue
		}
		name := p.Name
		if name == "" {
			name = p.ID
		}
		if prev, existed := oldByID[p.ID]; existed {
			if err := s.d.BuildCheck(prev); err == nil {
				keep = append(keep, prev)
				diff.Warnings = append(diff.Warnings, "profile_kept_old: "+name)
				dropped[p.ID] = true
				continue
			}
		}
		diff.Warnings = append(diff.Warnings, "profile_invalid: "+name)
		dropped[p.ID] = true
		if _, existed := oldByID[p.ID]; existed {
			diff.Removed = append(diff.Removed, p.ID)
		}
	}
	if len(keep) == 0 {
		return old, profile.Diff{}, fail(CodeInvalidProfiles, "none of the profiles in the subscription is valid")
	}
	diff.Added = without(diff.Added, dropped)
	diff.Changed = without(diff.Changed, dropped)
	next.Profiles = keep
	return next, diff, nil
}

func without(ids []string, drop map[string]bool) []string {
	var out []string
	for _, id := range ids {
		if !drop[id] {
			out = append(out, id)
		}
	}
	return out
}

// Remove deletes a subscription.
func (s *Service) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err := s.d.Store.Load()
	if err != nil {
		return fail(CodeStore, "cannot read the profile store")
	}
	out := subs[:0:0]
	for _, sub := range subs {
		if sub.ID != id {
			out = append(out, sub)
		}
	}
	if len(out) == len(subs) {
		return fail(CodeNotFound, "no such subscription")
	}
	if err := s.d.Store.Save(out); err != nil {
		return fail(CodeStore, "cannot save the profile store")
	}
	delete(s.sched, id)
	return nil
}

// SetUA changes a subscription's User-Agent; empty means the default.
func (s *Service) SetUA(id, ua string) error {
	if ua == "" {
		ua = subfetch.DefaultUA(s.d.Version, s.d.OS)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	subs, err := s.d.Store.Load()
	if err != nil {
		return fail(CodeStore, "cannot read the profile store")
	}
	found := false
	for i := range subs {
		if subs[i].ID == id {
			subs[i].UA = ua
			found = true
		}
	}
	if !found {
		return fail(CodeNotFound, "no such subscription")
	}
	if err := s.d.Store.Save(subs); err != nil {
		return fail(CodeStore, "cannot save the profile store")
	}
	return nil
}

// List returns copies of the subscriptions with each URL reduced to its host.
func (s *Service) List() []profile.Subscription {
	s.mu.Lock()
	subs, err := s.d.Store.Load()
	s.mu.Unlock()
	if err != nil {
		s.logf("subsvc: cannot read the profile store")
		return nil
	}
	out := make([]profile.Subscription, len(subs))
	for i, sub := range subs {
		out[i] = redacted(sub)
	}
	return out
}

func redacted(sub profile.Subscription) profile.Subscription {
	host := ""
	if u, err := url.Parse(sub.URL); err == nil {
		host = u.Host
	}
	sub.URL = host
	sub.Profiles = append([]profile.Profile(nil), sub.Profiles...)
	return sub
}
