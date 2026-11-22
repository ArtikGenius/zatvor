//go:build systemtest && windows

package sysproxy_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"

	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
	"github.com/ArtikGenius/zatvor/internal/systest"
)

func TestMain(m *testing.M) { systest.Main(m) }

// testPort is only written into the proxy setting; nothing listens on it.
const testPort = 18089

const internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// saveRunner saves the LAN and RAS proxy state of the runner and puts it
// back in t.Cleanup, which also runs when the test fails.
func saveRunner(t *testing.T, sp sysapi.SystemProxy) sysapi.ProxyState {
	t.Helper()
	names, err := sp.Connections()
	if err != nil {
		t.Fatalf("Connections = %v", err)
	}
	saved := map[string]sysapi.ProxyState{}
	for _, name := range append([]string{""}, names...) {
		s, err := sp.GetConn(name)
		if err != nil {
			t.Fatalf("GetConn(%q) = %v", name, err)
		}
		saved[name] = s
	}
	t.Logf("runner proxy state: %d RAS entries, LAN %s", len(names), mustJSON(t, saved[""]))
	t.Cleanup(func() {
		for name, s := range saved {
			if err := sp.SetConn(name, s); err != nil {
				t.Errorf("cleanup: restore %q: %v", name, err)
			}
		}
		if got, err := sp.Get(); err != nil || !reflect.DeepEqual(got, saved[""]) {
			t.Errorf("cleanup: LAN = %+v, %v; want %+v", got, err, saved[""])
		}
	})
	return saved[""]
}

func mustJSON(t *testing.T, s sysapi.ProxyState) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newManager(t *testing.T, sp sysapi.SystemProxy) *sysproxy.Manager {
	t.Helper()
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return sysproxy.New(sp, j)
}

type regValues struct {
	enable   uint64
	server   string
	override string
}

func readRegistry(t *testing.T) regValues {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open Internet Settings: %v", err)
	}
	defer k.Close()
	var v regValues
	if v.enable, _, err = k.GetIntegerValue("ProxyEnable"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("ProxyEnable: %v", err)
	}
	if v.server, _, err = k.GetStringValue("ProxyServer"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("ProxyServer: %v", err)
	}
	if v.override, _, err = k.GetStringValue("ProxyOverride"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("ProxyOverride: %v", err)
	}
	return v
}

// checkFlags logs the LAN's FLAGS and FLAGS_UI and, where FLAGS_UI is
// supported, asserts it equals want (want 0: log only).
func checkFlags(t *testing.T, when string, want uint32) {
	t.Helper()
	flags, ui, uiErr, err := sysproxy.RawFlags("")
	if err != nil {
		t.Fatalf("%s: read FLAGS: %v", when, err)
	}
	if uiErr != nil {
		t.Logf("%s: FLAGS=%#x, FLAGS_UI unsupported (%v)", when, flags, uiErr)
		return
	}
	t.Logf("%s: FLAGS=%#x FLAGS_UI=%#x", when, flags, ui)
	if want != 0 && ui != want {
		t.Fatalf("%s: FLAGS_UI = %#x, want %#x", when, ui, want)
	}
}

func assertRestored(t *testing.T, m *sysproxy.Manager) {
	t.Helper()
	res := m.LastRevert()
	if len(res) == 0 {
		t.Fatal("LastRevert is empty")
	}
	for _, r := range res {
		if r.Outcome != journal.Restored {
			t.Fatalf("revert of %s = %s (%v), want restored", r.Target, r.Outcome, r.Err)
		}
	}
}

func TestLiveApplyRevert(t *testing.T) {
	systest.Require(t)
	sp := sysproxy.NewWindows()
	saved := saveRunner(t, sp)
	m := newManager(t, sp)
	ours := sysproxy.Ours(testPort)
	checkFlags(t, "runner", 0)

	if err := m.Apply(ours, true); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	checkFlags(t, "after Apply", sysproxy.FlagsOurs)
	got, err := sp.Get()
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(t, got) != mustJSON(t, ours) {
		t.Fatalf("InternetQueryOptionW after Apply = %s, want %s", mustJSON(t, got), mustJSON(t, ours))
	}
	reg := readRegistry(t)
	if reg.enable != 1 || reg.server != ours.Server || reg.override != strings.Join(ours.Bypass, ";") {
		t.Fatalf("registry after Apply = %+v, want enable 1, server %q, override %q", reg, ours.Server, strings.Join(ours.Bypass, ";"))
	}
	if d, cur, err := m.Drifted(); err != nil || d {
		t.Fatalf("Drifted right after Apply = %v (%s), %v; want false", d, mustJSON(t, cur), err)
	}

	if err := m.Revert(); err != nil {
		t.Fatalf("Revert = %v", err)
	}
	assertRestored(t, m)
	checkFlags(t, "after Revert", 0)
	if got, err := sp.Get(); err != nil || mustJSON(t, got) != mustJSON(t, saved) {
		t.Fatalf("LAN after Revert = %+v, %v; want %s", got, err, mustJSON(t, saved))
	}
	if reg := readRegistry(t); (reg.enable == 1) != saved.Enabled {
		t.Fatalf("registry ProxyEnable after Revert = %d, want enabled=%v", reg.enable, saved.Enabled)
	}
}

func TestLiveRestoresForeignPAC(t *testing.T) {
	systest.Require(t)
	sp := sysproxy.NewWindows()
	saveRunner(t, sp)
	foreign := sysapi.ProxyState{
		Server:     "192.0.2.10:3128", // TEST-NET-1, stored but not enabled
		Bypass:     []string{"z.example.test", "<local>", "a.example.test"},
		PACURL:     "http://127.0.0.1:9/zatvor-systemtest.pac",
		AutoDetect: true,
	}
	if err := sp.Set(foreign); err != nil {
		t.Fatalf("Set(foreign) = %v", err)
	}
	checkFlags(t, "after Set(foreign)", 0x1|0x4|0x8) // DIRECT|AUTO_PROXY_URL|AUTO_DETECT
	if got, err := sp.Get(); err != nil || mustJSON(t, got) != mustJSON(t, foreign) {
		t.Fatalf("Get after Set(foreign) = %+v, %v; want %s", got, err, mustJSON(t, foreign))
	}

	m := newManager(t, sp)
	ours := sysproxy.Ours(testPort)
	var ce *sysproxy.ConflictError
	if err := m.Apply(ours, false); !errors.As(err, &ce) {
		t.Fatalf("Apply over a foreign PAC = %v, want *ConflictError", err)
	}
	if got, _ := sp.Get(); mustJSON(t, got) != mustJSON(t, foreign) {
		t.Fatalf("conflicting Apply changed the LAN to %s", mustJSON(t, got))
	}
	if err := m.Apply(ours, true); err != nil {
		t.Fatalf("forced Apply = %v", err)
	}
	if got, _ := sp.Get(); mustJSON(t, got) != mustJSON(t, ours) {
		t.Fatalf("LAN after forced Apply = %s, want %s", mustJSON(t, got), mustJSON(t, ours))
	}
	if err := m.Revert(); err != nil {
		t.Fatalf("Revert = %v", err)
	}
	assertRestored(t, m)
	checkFlags(t, "after Revert", 0x1|0x4|0x8)
	if got, err := sp.Get(); err != nil || mustJSON(t, got) != mustJSON(t, foreign) {
		t.Fatalf("LAN after Revert = %+v, %v; want byte-equal %s", got, err, mustJSON(t, foreign))
	}
}
