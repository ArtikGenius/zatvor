package sysproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

// fakeSP is an in-memory SystemProxy: the LAN under name "" plus RAS entries.
type fakeSP struct {
	mu     sync.Mutex
	names  []string
	states map[string]sysapi.ProxyState
	sets   []string // connection names, in Set order
	onSet  func(name string, s sysapi.ProxyState)

	connErr error // returned by Connections when set
}

func newFake(lan sysapi.ProxyState, ras map[string]sysapi.ProxyState) *fakeSP {
	f := &fakeSP{states: map[string]sysapi.ProxyState{"": clone(lan)}}
	for name, s := range ras {
		f.names = append(f.names, name)
		f.states[name] = clone(s)
	}
	slices.Sort(f.names)
	return f
}

func (f *fakeSP) Get() (sysapi.ProxyState, error) { return f.GetConn("") }
func (f *fakeSP) Set(s sysapi.ProxyState) error   { return f.SetConn("", s) }
func (f *fakeSP) Connections() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connErr != nil {
		return nil, f.connErr
	}
	return slices.Clone(f.names), nil
}
func (f *fakeSP) state(name string) sysapi.ProxyState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.states[name])
}

func (f *fakeSP) GetConn(name string) (sysapi.ProxyState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.states[name]; ok {
		return clone(s), nil
	}
	return sysapi.ProxyState{}, fmt.Errorf("no connection %q", name)
}

func (f *fakeSP) SetConn(name string, s sysapi.ProxyState) error {
	f.mu.Lock()
	onSet := f.onSet
	f.mu.Unlock()
	if onSet != nil {
		onSet(name, s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.states[name]; !ok {
		return fmt.Errorf("no connection %q", name)
	}
	f.states[name] = clone(s)
	f.sets = append(f.sets, name)
	return nil
}

// otherWriter changes a connection as another program would.
func (f *fakeSP) otherWriter(name string, s sysapi.ProxyState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[name] = clone(s)
}

func openJournal(t *testing.T) *journal.Journal {
	t.Helper()
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func pending(t *testing.T, j *journal.Journal) []journal.Entry {
	t.Helper()
	p, err := j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustEqual(t *testing.T, what string, got, want sysapi.ProxyState) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %+v, want %+v", what, got, want)
	}
}

var userLAN = sysapi.ProxyState{
	Server:     "corp-proxy.example.test:3128",
	Bypass:     []string{"*.corp.example.test", "<local>", "10.*"},
	AutoDetect: true,
}

func TestOurs(t *testing.T) {
	got := Ours(10808)
	want := sysapi.ProxyState{
		Enabled: true,
		Server:  "127.0.0.1:10808",
		Bypass: []string{"<local>", "localhost", "127.*", "10.*",
			"172.16.*", "172.17.*", "172.18.*", "172.19.*", "172.20.*", "172.21.*", "172.22.*", "172.23.*",
			"172.24.*", "172.25.*", "172.26.*", "172.27.*", "172.28.*", "172.29.*", "172.30.*", "172.31.*",
			"192.168.*"},
	}
	mustEqual(t, "Ours(10808)", got, want)
}

func TestApplyJournalsBeforeSet(t *testing.T) {
	j := openJournal(t)
	f := newFake(userLAN, map[string]sysapi.ProxyState{"Office VPN": {}})
	var order []string
	f.onSet = func(name string, s sysapi.ProxyState) {
		target := targetOf(name)
		for _, e := range pending(t, j) {
			var applied sysapi.ProxyState
			if err := json.Unmarshal(e.Applied, &applied); err != nil {
				t.Fatal(err)
			}
			if e.Kind == Kind && e.Target == target && reflect.DeepEqual(applied, s) {
				order = append(order, "journaled "+target)
			}
		}
		order = append(order, "set "+target)
	}
	m := New(f, j)
	if err := m.Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"journaled lan", "set lan", "journaled ras:Office VPN", "set ras:Office VPN"}
	if !slices.Equal(order, want) {
		t.Fatalf("call order = %q, want %q", order, want)
	}
	p := pending(t, j)
	if len(p) != 2 {
		t.Fatalf("pending = %d entries, want 2", len(p))
	}
	var prev sysapi.ProxyState
	if err := json.Unmarshal(p[0].Prev, &prev); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "journaled LAN prev", prev, userLAN)
	mustEqual(t, "LAN after Apply", f.state(""), Ours(10808))
}

func TestForeignPACConflict(t *testing.T) {
	pac := sysapi.ProxyState{PACURL: "http://wpad.example.test/proxy.pac", Bypass: []string{"<local>"}}
	foreign := sysapi.ProxyState{Enabled: true, Server: "127.0.0.1:8888"} // loopback, but not ours
	for _, tc := range []struct {
		name string
		lan  sysapi.ProxyState
	}{{"pac", pac}, {"foreign server", foreign}} {
		t.Run(tc.name, func(t *testing.T) {
			j := openJournal(t)
			f := newFake(tc.lan, nil)
			m := New(f, j)
			err := m.Apply(Ours(10808), false)
			var ce *ConflictError
			if !errors.As(err, &ce) {
				t.Fatalf("Apply = %v, want *ConflictError", err)
			}
			mustEqual(t, "ConflictError.Current", ce.Current, tc.lan)
			if len(f.sets) != 0 || len(pending(t, j)) != 0 {
				t.Fatalf("conflict still set %q / journaled %d", f.sets, len(pending(t, j)))
			}

			if err := m.Apply(Ours(10808), true); err != nil {
				t.Fatalf("forced Apply = %v", err)
			}
			mustEqual(t, "LAN after forced Apply", f.state(""), Ours(10808))
			if err := m.Revert(); err != nil {
				t.Fatal(err)
			}
			mustEqual(t, "LAN after Revert", f.state(""), tc.lan)
		})
	}
}

func TestNoConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		lan  sysapi.ProxyState
	}{
		{"default", sysapi.ProxyState{AutoDetect: true}},
		{"disabled server", sysapi.ProxyState{Server: "corp-proxy.example.test:3128"}},
		{"ours", Ours(10808)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(tc.lan, nil)
			if err := New(f, openJournal(t)).Apply(Ours(10808), false); err != nil {
				t.Fatalf("Apply = %v, want no conflict", err)
			}
			mustEqual(t, "LAN", f.state(""), Ours(10808))
		})
	}
}

func TestReapplyNewPortIsNotConflict(t *testing.T) {
	j := openJournal(t)
	f := newFake(userLAN, nil)
	if err := New(f, j).Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	// A new process (after a crash or a port re-pick) sees our old port.
	m := New(f, j)
	if err := m.Apply(Ours(20808), false); err != nil {
		t.Fatalf("re-apply with new port = %v, want no conflict", err)
	}
	mustEqual(t, "LAN", f.state(""), Ours(20808))
	if err := m.Revert(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "LAN after Revert", f.state(""), userLAN)
	if p := pending(t, j); len(p) != 0 {
		t.Fatalf("pending after Revert = %d, want 0", len(p))
	}
}

func TestApplyRejectsNonProxyState(t *testing.T) {
	f := newFake(userLAN, nil)
	m := New(f, openJournal(t))
	for _, s := range []sysapi.ProxyState{
		{},
		{Enabled: true},
		{Enabled: true, Server: "127.0.0.1:1", PACURL: "http://pac.example.test/"},
	} {
		if err := m.Apply(s, true); err == nil {
			t.Fatalf("Apply(%+v) = nil, want error", s)
		}
	}
	if len(f.sets) != 0 {
		t.Fatalf("invalid Apply set %q", f.sets)
	}
}

func TestApplyClearsAutoDetect(t *testing.T) {
	f := newFake(sysapi.ProxyState{AutoDetect: true}, nil)
	s := Ours(10808)
	s.AutoDetect = true
	if err := New(f, openJournal(t)).Apply(s, false); err != nil {
		t.Fatal(err)
	}
	if f.state("").AutoDetect {
		t.Fatal("AutoDetect still on after Apply")
	}
}

func TestRevertRestoresExactPrev(t *testing.T) {
	prev := sysapi.ProxyState{
		Enabled:    true,
		Server:     "http=a.example.test:80;https=b.example.test:443",
		Bypass:     []string{"z.example.test", "<local>", "a.example.test", "", " spaced "},
		PACURL:     "http://wpad.example.test/proxy.pac?x=1&y=2",
		AutoDetect: true,
	}
	j := openJournal(t)
	f := newFake(prev, nil)
	m := New(f, j)
	if err := m.Apply(Ours(10808), true); err != nil {
		t.Fatal(err)
	}
	if err := m.Revert(); err != nil {
		t.Fatal(err)
	}
	got := f.state("")
	mustEqual(t, "LAN after Revert", got, prev)
	gb, _ := json.Marshal(got)
	pb, _ := json.Marshal(prev)
	if string(gb) != string(pb) {
		t.Fatalf("restored %s, want byte-equal %s", gb, pb)
	}
	res := m.LastRevert()
	if len(res) != 1 || res[0].Outcome != journal.Restored {
		t.Fatalf("LastRevert = %+v, want one Restored", res)
	}
}

func TestRevertAfterCrashFromNewManager(t *testing.T) {
	dir := t.TempDir()
	j, _ := journal.Open(dir)
	f := newFake(userLAN, nil)
	if err := New(f, j).Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	// The process dies; the next start opens the same journal.
	j2, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(f, j2).Revert(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "LAN after crash revert", f.state(""), userLAN)
}

func TestDriftDetected(t *testing.T) {
	f := newFake(userLAN, nil)
	m := New(f, openJournal(t))
	if d, _, err := m.Drifted(); err != nil || d {
		t.Fatalf("Drifted before Apply = %v, %v; want false", d, err)
	}
	if err := m.Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	if d, _, err := m.Drifted(); err != nil || d {
		t.Fatalf("Drifted right after Apply = %v, %v; want false", d, err)
	}
	theirs := sysapi.ProxyState{Enabled: true, Server: "other-app.example.test:8080"}
	f.otherWriter("", theirs)
	d, cur, err := m.Drifted()
	if err != nil || !d {
		t.Fatalf("Drifted after another writer = %v, %v; want true", d, err)
	}
	mustEqual(t, "Drifted current", cur, theirs)
}

func TestRevertAfterDriftKeepsTheirs(t *testing.T) {
	j := openJournal(t)
	f := newFake(userLAN, nil)
	m := New(f, j)
	if err := m.Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	theirs := sysapi.ProxyState{PACURL: "http://other-app.example.test/p.pac"}
	f.otherWriter("", theirs)
	if err := m.Revert(); err != nil {
		t.Fatalf("Revert = %v, want nil (skipping is not an error)", err)
	}
	mustEqual(t, "LAN after Revert", f.state(""), theirs)
	res := m.LastRevert()
	if len(res) != 1 || res[0].Outcome != journal.SkippedChangedByOther || res[0].Target != "lan" {
		t.Fatalf("LastRevert = %+v, want one SkippedChangedByOther on lan", res)
	}
	if p := pending(t, j); len(p) != 0 {
		t.Fatalf("pending after skip = %d, want 0", len(p))
	}
	if d, _, _ := m.Drifted(); d {
		t.Fatal("Drifted after Revert = true; nothing of ours is applied")
	}
}

func TestRASEntriesCovered(t *testing.T) {
	vpn := sysapi.ProxyState{PACURL: "http://vpn.example.test/p.pac", Bypass: []string{"b", "a"}}
	dial := sysapi.ProxyState{Server: "dial.example.test:80", AutoDetect: true}
	j := openJournal(t)
	f := newFake(userLAN, map[string]sysapi.ProxyState{"Office VPN": vpn, "Dial-up": dial})
	m := New(f, j)
	if err := m.Apply(Ours(10808), true); err != nil { // the VPN PAC is foreign
		t.Fatal(err)
	}
	var targets []string
	for _, e := range pending(t, j) {
		targets = append(targets, e.Target)
	}
	if want := []string{"lan", "ras:Dial-up", "ras:Office VPN"}; !slices.Equal(targets, want) {
		t.Fatalf("journaled targets = %q, want %q", targets, want)
	}
	for _, n := range []string{"", "Dial-up", "Office VPN"} {
		mustEqual(t, "after Apply "+n, f.state(n), Ours(10808))
	}
	if err := m.Revert(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "LAN after Revert", f.state(""), userLAN)
	mustEqual(t, "Office VPN after Revert", f.state("Office VPN"), vpn)
	mustEqual(t, "Dial-up after Revert", f.state("Dial-up"), dial)
	if n := len(m.LastRevert()); n != 3 {
		t.Fatalf("LastRevert has %d results, want 3", n)
	}
}

func TestRASEntryRemovedIsSkipped(t *testing.T) {
	j := openJournal(t)
	f := newFake(userLAN, map[string]sysapi.ProxyState{"Office VPN": {}})
	m := New(f, j)
	if err := m.Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.names = nil
	delete(f.states, "Office VPN")
	f.mu.Unlock()
	if err := m.Revert(); err != nil {
		t.Fatalf("Revert = %v, want nil", err)
	}
	if p := pending(t, j); len(p) != 0 {
		t.Fatalf("pending = %+v, want none", p)
	}
	mustEqual(t, "LAN after Revert", f.state(""), userLAN)
}

func TestRevertIgnoresOtherKinds(t *testing.T) {
	j := openJournal(t)
	if _, err := j.Begin(journal.Entry{Kind: "devtools", Target: "x", Prev: json.RawMessage(`1`), Applied: json.RawMessage(`2`)}); err != nil {
		t.Fatal(err)
	}
	f := newFake(userLAN, nil)
	m := New(f, j)
	if err := m.Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	if err := m.Revert(); err != nil {
		t.Fatalf("Revert = %v, want nil despite a devtools entry", err)
	}
	for _, r := range m.LastRevert() {
		if r.Kind != Kind {
			t.Fatalf("LastRevert reports kind %q", r.Kind)
		}
	}
	p := pending(t, j)
	if len(p) != 1 || p[0].Kind != "devtools" {
		t.Fatalf("pending = %+v, want only the devtools entry", p)
	}
}

// failingSP fails SetConn for one connection.
type failingSP struct {
	*fakeSP
	fail string
}

func (f *failingSP) SetConn(name string, s sysapi.ProxyState) error {
	if name == f.fail {
		return errors.New("access denied")
	}
	return f.fakeSP.SetConn(name, s)
}

func TestRevertReportsFailure(t *testing.T) {
	j := openJournal(t)
	base := newFake(userLAN, nil)
	if err := New(base, j).Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	m := New(&failingSP{fakeSP: base, fail: ""}, j)
	if err := m.Revert(); err == nil {
		t.Fatal("Revert = nil, want the restore failure")
	}
	if p := pending(t, j); len(p) != 1 {
		t.Fatalf("pending = %d, want the failed entry kept", len(p))
	}
}

func TestReverterTargets(t *testing.T) {
	f := newFake(userLAN, map[string]sysapi.ProxyState{"Office VPN": {}})
	r := New(f, openJournal(t)).Reverter()
	if _, err := r.Current("bogus"); err == nil {
		t.Fatal("Current(bogus) = nil error")
	}
	cur, err := r.Current("lan")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(userLAN)
	if string(cur) != string(want) {
		t.Fatalf("Current(lan) = %s, want %s", cur, want)
	}
	if err := r.Restore("ras:Office VPN", want); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "Office VPN after Restore", f.state("Office VPN"), userLAN)
}

func TestRASConflict(t *testing.T) {
	vpn := sysapi.ProxyState{PACURL: "http://vpn.example.test/p.pac"}
	j := openJournal(t)
	f := newFake(sysapi.ProxyState{AutoDetect: true}, map[string]sysapi.ProxyState{"Dial-up": {}, "Office VPN": vpn})
	m := New(f, j)
	err := m.Apply(Ours(10808), false)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("Apply = %v, want *ConflictError", err)
	}
	if ce.Conn != "Office VPN" {
		t.Fatalf("ConflictError.Conn = %q, want %q", ce.Conn, "Office VPN")
	}
	mustEqual(t, "ConflictError.Current", ce.Current, vpn)
	if len(f.sets) != 0 || len(pending(t, j)) != 0 {
		t.Fatalf("RAS conflict still set %q / journaled %d", f.sets, len(pending(t, j)))
	}
	if err := m.Apply(Ours(10808), true); err != nil {
		t.Fatalf("forced Apply = %v", err)
	}
	mustEqual(t, "Office VPN after forced Apply", f.state("Office VPN"), Ours(10808))
	if err := m.Revert(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "Office VPN after Revert", f.state("Office VPN"), vpn)
}

func TestLANConflictNamesLAN(t *testing.T) {
	f := newFake(sysapi.ProxyState{PACURL: "http://wpad.example.test/p.pac"}, map[string]sysapi.ProxyState{"Office VPN": {}})
	var ce *ConflictError
	if err := New(f, openJournal(t)).Apply(Ours(10808), false); !errors.As(err, &ce) || ce.Conn != "" {
		t.Fatalf("Apply = %v, want *ConflictError with Conn \"\"", err)
	}
}

func TestRASUnavailableApplyIsLANOnly(t *testing.T) {
	j := openJournal(t)
	f := newFake(userLAN, nil)
	f.connErr = fmt.Errorf("load rasapi32.dll: %w", sysapi.ErrRASUnavailable)
	m := New(f, j)
	if err := m.Apply(Ours(10808), false); err != nil {
		t.Fatalf("Apply without RAS = %v, want LAN-only apply", err)
	}
	if p := pending(t, j); len(p) != 1 || p[0].Target != "lan" {
		t.Fatalf("pending = %+v, want only lan", p)
	}
	if err := m.Revert(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "LAN after Revert", f.state(""), userLAN)
}

func TestConnectionsErrorFailsApply(t *testing.T) {
	f := newFake(userLAN, nil)
	f.connErr = errors.New("RasEnumEntriesW: access denied")
	if err := New(f, openJournal(t)).Apply(Ours(10808), false); err == nil {
		t.Fatal("Apply = nil, want the Connections error")
	}
	if len(f.sets) != 0 {
		t.Fatalf("Apply set %q despite the error", f.sets)
	}
}

func TestRASUnavailableAtRevertKeepsEntries(t *testing.T) {
	j := openJournal(t)
	f := newFake(userLAN, map[string]sysapi.ProxyState{"Office VPN": {Server: "vpn.example.test:80"}})
	if err := New(f, j).Apply(Ours(10808), false); err != nil {
		t.Fatal(err)
	}
	f.connErr = fmt.Errorf("load rasapi32.dll: %w", sysapi.ErrRASUnavailable)
	m := New(f, j)
	if err := m.Revert(); err == nil {
		t.Fatal("Revert = nil, want the RAS failure")
	}
	mustEqual(t, "LAN after Revert", f.state(""), userLAN)
	p := pending(t, j)
	if len(p) != 1 || p[0].Target != "ras:Office VPN" {
		t.Fatalf("pending = %+v, want the ras entry kept", p)
	}
	// RAS is back: the next Revert restores the entry.
	f.connErr = nil
	if err := m.Revert(); err != nil {
		t.Fatal(err)
	}
	mustEqual(t, "Office VPN after second Revert", f.state("Office VPN"), sysapi.ProxyState{Server: "vpn.example.test:80"})
}

func TestApplyFailsMidwayThenRevert(t *testing.T) {
	j := openJournal(t)
	base := newFake(userLAN, map[string]sysapi.ProxyState{"Office VPN": {Server: "vpn.example.test:80"}})
	sp := &failingSP{fakeSP: base, fail: "Office VPN"}
	m := New(sp, j)
	if err := m.Apply(Ours(10808), false); err == nil {
		t.Fatal("Apply = nil, want the RAS set failure")
	}
	mustEqual(t, "LAN after failed Apply", base.state(""), Ours(10808))
	if err := m.Revert(); err != nil {
		t.Fatalf("Revert = %v", err)
	}
	mustEqual(t, "LAN after Revert", base.state(""), userLAN)
	mustEqual(t, "Office VPN after Revert", base.state("Office VPN"), sysapi.ProxyState{Server: "vpn.example.test:80"})
	res := m.LastRevert()
	if len(res) != 2 {
		t.Fatalf("LastRevert = %+v, want 2 results", res)
	}
	for _, r := range res {
		if r.Outcome != journal.Restored {
			t.Fatalf("revert of %s = %s, want restored", r.Target, r.Outcome)
		}
	}
	if p := pending(t, j); len(p) != 0 {
		t.Fatalf("pending = %d, want 0", len(p))
	}
}
