// Package sysproxy points the OS system proxy at our local inbound and puts
// the user's own settings back afterwards (spec §8). Every change is
// journaled before it is made, a foreign proxy or PAC is never replaced
// without an explicit force, and a value someone else wrote after us is
// left alone on revert.
package sysproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

// Kind is the journal Kind of our entries.
const Kind = "sysproxy"

const (
	lanTarget = "lan"
	rasPrefix = "ras:"
)

// oursBypass is the bypass list we set: local names and private ranges.
var oursBypass = func() []string {
	b := []string{"<local>", "localhost", "127.*", "10.*"}
	for i := 16; i <= 31; i++ {
		b = append(b, "172."+strconv.Itoa(i)+".*")
	}
	return append(b, "192.168.*")
}()

// Ours is the state we apply: our mixed inbound on loopback, no PAC, no
// auto-detection.
func Ours(mixedPort int) sysapi.ProxyState {
	return sysapi.ProxyState{
		Enabled: true,
		Server:  net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort)),
		Bypass:  slices.Clone(oursBypass),
	}
}

// ConflictError reports a foreign proxy or PAC on a connection: Conn is
// "" for the LAN settings or the name of a RAS entry. Apply changed
// nothing; the user decides whether to force.
type ConflictError struct {
	Conn    string
	Current sysapi.ProxyState
}

func (e *ConflictError) Error() string {
	what := "system proxy"
	if e.Current.PACURL != "" {
		what = "PAC script"
	}
	where := "the LAN settings"
	if e.Conn != "" {
		where = "a dial-up/VPN connection"
	}
	return "sysproxy: a foreign " + what + " is set on " + where
}

// Manager applies and reverts our system proxy through a journal.
type Manager struct {
	sp sysapi.SystemProxy
	j  *journal.Journal

	mu      sync.Mutex
	applied *sysapi.ProxyState     // LAN state last applied by this process
	last    []journal.RevertResult // sysproxy results of the last Revert
}

// New returns a Manager over sp that journals into j.
func New(sp sysapi.SystemProxy, j *journal.Journal) *Manager {
	return &Manager{sp: sp, j: j}
}

func targetOf(name string) string {
	if name == "" {
		return lanTarget
	}
	return rasPrefix + name
}

func connOf(target string) (string, error) {
	switch {
	case target == lanTarget:
		return "", nil
	case strings.HasPrefix(target, rasPrefix) && len(target) > len(rasPrefix):
		return strings.TrimPrefix(target, rasPrefix), nil
	}
	return "", fmt.Errorf("sysproxy: unknown target %q", target)
}

// Apply sets ours on the LAN settings and on every RAS entry. Before each
// connection is set, its previous state is journaled (fsynced). When any
// connection holds a foreign proxy or PAC and force is false, Apply
// returns *ConflictError for the first one and changes nothing. Without
// RAS (sysapi.ErrRASUnavailable) only the LAN is set. If Apply fails
// midway, the connections already set are journaled; call Revert.
func (m *Manager) Apply(ours sysapi.ProxyState, force bool) error {
	if !ours.Enabled || ours.Server == "" || ours.PACURL != "" {
		return errors.New("sysproxy: Apply needs an enabled proxy server and no PAC")
	}
	ours = clone(ours)
	ours.AutoDetect = false
	applied, err := json.Marshal(ours)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	names, err := m.sp.Connections()
	if errors.Is(err, sysapi.ErrRASUnavailable) {
		names, err = nil, nil
	}
	if err != nil {
		return fmt.Errorf("sysproxy: list connections: %w", err)
	}
	names = append([]string{""}, names...)
	prevs := make([]sysapi.ProxyState, len(names))
	for i, name := range names {
		if prevs[i], err = m.sp.GetConn(name); err != nil {
			return fmt.Errorf("sysproxy: read %s: %w", targetOf(name), err)
		}
		if force {
			continue
		}
		foreign, err := m.foreign(prevs[i], ours)
		if err != nil {
			return err
		}
		if foreign {
			return &ConflictError{Conn: name, Current: prevs[i]}
		}
	}

	for i, name := range names {
		pb, err := json.Marshal(prevs[i])
		if err != nil {
			return err
		}
		if _, err := m.j.Begin(journal.Entry{Kind: Kind, Target: targetOf(name), Prev: pb, Applied: applied}); err != nil {
			return fmt.Errorf("sysproxy: journal %s: %w", targetOf(name), err)
		}
		if err := m.sp.SetConn(name, ours); err != nil {
			return fmt.Errorf("sysproxy: set %s: %w", targetOf(name), err)
		}
		if i == 0 {
			s := clone(ours)
			m.applied = &s
		}
	}
	return nil
}

// foreign reports whether a connection's state belongs to someone else: a PAC
// URL, or an enabled server that is neither ours nor a loopback server a
// pending journal entry of ours applied (a port re-pick or a crash).
// Auto-detection alone is the Windows default and not foreign.
func (m *Manager) foreign(cur, ours sysapi.ProxyState) (bool, error) {
	if cur.PACURL != "" {
		return true, nil
	}
	if !cur.Enabled || cur.Server == "" || cur.Server == ours.Server {
		return false, nil
	}
	if host, _, err := net.SplitHostPort(cur.Server); err != nil || host != "127.0.0.1" {
		return true, nil
	}
	p, err := m.j.Pending()
	if err != nil {
		return false, fmt.Errorf("sysproxy: read journal: %w", err)
	}
	for _, e := range p {
		var s sysapi.ProxyState
		if e.Kind == Kind && json.Unmarshal(e.Applied, &s) == nil && s.Server == cur.Server {
			return false, nil
		}
	}
	return true, nil
}

// Revert restores every pending sysproxy entry whose connection still holds
// what we applied; one changed by someone else is kept as it is (see
// LastRevert). Entries of other kinds are left pending. The error joins the
// failed restores only.
func (m *Manager) Revert() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		ours    []journal.RevertResult
		errs    []error
		lanFail bool
	)
	for _, r := range m.j.RevertAll(map[string]journal.Reverter{Kind: m.Reverter()}) {
		if r.Kind != Kind && r.Kind != "" { // "" is a journal read failure
			continue
		}
		ours = append(ours, r)
		if r.Outcome == journal.Failed {
			errs = append(errs, fmt.Errorf("sysproxy: revert %s: %w", r.Target, r.Err))
			lanFail = lanFail || r.Target == lanTarget || r.Kind == ""
		}
	}
	m.last = ours
	if !lanFail {
		m.applied = nil
	}
	return errors.Join(errs...)
}

// LastRevert returns the sysproxy results of the last Revert, so the caller
// can warn about connections left to the program that changed them.
func (m *Manager) LastRevert() []journal.RevertResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.last)
}

// Drifted reports whether the LAN settings differ from what this process
// last applied, and returns them. It is false when nothing is applied.
func (m *Manager) Drifted() (bool, sysapi.ProxyState, error) {
	m.mu.Lock()
	applied := m.applied
	m.mu.Unlock()
	if applied == nil {
		return false, sysapi.ProxyState{}, nil
	}
	cur, err := m.sp.Get()
	if err != nil {
		return false, sysapi.ProxyState{}, fmt.Errorf("sysproxy: read LAN settings: %w", err)
	}
	same, err := equal(cur, *applied)
	if err != nil {
		return false, cur, err
	}
	return !same, cur, nil
}

// equal compares two states as the journal does: by their JSON.
func equal(a, b sysapi.ProxyState) (bool, error) {
	ab, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}

// Reverter is the journal.Reverter for Kind. Targets are "lan" and
// "ras:<entry name>". A restore is exact at the ProxyState level; see
// NewWindows for what the OS may store differently.
func (m *Manager) Reverter() journal.Reverter { return reverter{sp: m.sp} }

type reverter struct{ sp sysapi.SystemProxy }

// gone is the Current of a RAS entry that no longer exists. It matches
// neither Applied nor Prev, so the entry is closed untouched.
var gone = json.RawMessage(`{"gone":true}`)

func (r reverter) Current(target string) (json.RawMessage, error) {
	name, err := connOf(target)
	if err != nil {
		return nil, err
	}
	if name != "" {
		// An error (RAS unavailable included) fails the entry, which then
		// stays pending with its Prev; it is never taken as "gone".
		names, err := r.sp.Connections()
		if err != nil {
			return nil, err
		}
		if !slices.Contains(names, name) {
			return gone, nil
		}
	}
	s, err := r.sp.GetConn(name)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func (r reverter) Restore(target string, prev json.RawMessage) error {
	name, err := connOf(target)
	if err != nil {
		return err
	}
	var s sysapi.ProxyState
	if err := json.Unmarshal(prev, &s); err != nil {
		return fmt.Errorf("sysproxy: journaled state of %s: %w", target, err)
	}
	return r.sp.SetConn(name, s)
}

func clone(s sysapi.ProxyState) sysapi.ProxyState {
	s.Bypass = slices.Clone(s.Bypass)
	return s
}
