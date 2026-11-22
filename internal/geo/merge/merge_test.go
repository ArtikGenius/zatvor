package merge

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

// writeSource writes a source's dat files. site maps code -> one domain value,
// ip maps code -> one /32 first octet-quad.
func writeSource(t *testing.T, name string, site map[string]string, ip map[string][4]byte) Source {
	t.Helper()
	dir := t.TempDir()
	sl := &geodata.GeoSiteList{}
	for code, v := range site {
		sl.Entry = append(sl.Entry, &geodata.GeoSite{Code: code, Domain: []*geodata.Domain{{Type: geodata.Domain_Domain, Value: v}}})
	}
	il := &geodata.GeoIPList{}
	for code, a := range ip {
		il.Entry = append(il.Entry, &geodata.GeoIP{Code: code, Cidr: []*geodata.CIDR{{Ip: a[:], Prefix: 32}}})
	}
	s := Source{Name: name, GeoIP: filepath.Join(dir, "geoip.dat"), GeoSite: filepath.Join(dir, "geosite.dat")}
	for p, m := range map[string]proto.Message{s.GeoSite: sl, s.GeoIP: il} {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func readSite(t *testing.T, path string) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var l geodata.GeoSiteList
	if err := proto.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, e := range l.Entry {
		for _, d := range e.Domain {
			out[e.Code] = append(out[e.Code], d.Value)
		}
		if len(e.Domain) == 0 {
			out[e.Code] = nil
		}
	}
	return out
}

func readIP(t *testing.T, path string) map[string][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var l geodata.GeoIPList
	if err := proto.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range l.Entry {
		out[e.Code] = e.Cidr[0].Ip
	}
	return out
}

func TestPrimaryWins(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"GOOGLE": "p.example"}, map[string][4]byte{"RU": {1, 1, 1, 1}})
	f := writeSource(t, "v2fly", map[string]string{"GOOGLE": "f.example"}, map[string][4]byte{"RU": {2, 2, 2, 2}})
	dst := t.TempDir()
	res, err := Merge(dst, []string{"google"}, []string{"ru"}, p, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, res.GeoSite); !reflect.DeepEqual(got, map[string][]string{"GOOGLE": {"p.example"}}) {
		t.Errorf("site = %v", got)
	}
	if got := readIP(t, res.GeoIP); !reflect.DeepEqual(got, map[string][]byte{"RU": {1, 1, 1, 1}}) {
		t.Errorf("ip = %v", got)
	}
	if res.From["geosite:google"] != "roscomvpn" || res.From["geoip:ru"] != "roscomvpn" {
		t.Errorf("From = %v", res.From)
	}
}

func TestFallbackFills(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"GOOGLE": "p.example"}, nil)
	f := writeSource(t, "v2fly", map[string]string{"CN": "f.example"}, map[string][4]byte{"PRIVATE": {10, 0, 0, 0}})
	res, err := Merge(t.TempDir(), []string{"google", "cn"}, []string{"private"}, p, f)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"GOOGLE": {"p.example"}, "CN": {"f.example"}}
	if got := readSite(t, res.GeoSite); !reflect.DeepEqual(got, want) {
		t.Errorf("site = %v", got)
	}
	if res.From["geosite:cn"] != "v2fly" || res.From["geosite:google"] != "roscomvpn" || res.From["geoip:private"] != "v2fly" {
		t.Errorf("From = %v", res.From)
	}
}

func TestMissingTag(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"GOOGLE": "p"}, nil)
	f := writeSource(t, "v2fly", map[string]string{"CN": "f"}, nil)
	for _, tc := range []struct {
		site, ip []string
		want     string
		kind     string
	}{
		{[]string{"google", "win-spy"}, nil, `geosite tag "win-spy" is missing from roscomvpn and v2fly`, "geosite"},
		{nil, []string{"xx"}, `geoip tag "xx" is missing from roscomvpn and v2fly`, "geoip"},
	} {
		_, err := Merge(t.TempDir(), tc.site, tc.ip, p, f)
		var me *MissingTagError
		if !errors.As(err, &me) {
			t.Fatalf("err = %v, want *MissingTagError", err)
		}
		if err.Error() != tc.want || me.Kind != tc.kind || !reflect.DeepEqual(me.Searched, []string{"roscomvpn", "v2fly"}) {
			t.Errorf("err = %q kind=%s searched=%v", err, me.Kind, me.Searched)
		}
	}
}

func TestCaseAndAttributeSuffix(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"Google": "p"}, nil)
	f := writeSource(t, "v2fly", nil, nil)
	res, err := Merge(t.TempDir(), []string{"google@cn", "GOOGLE"}, nil, p, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := readSite(t, res.GeoSite); !reflect.DeepEqual(got, map[string][]string{"Google": {"p"}}) {
		t.Errorf("site = %v (source spelling must be kept, once)", got)
	}
}

// A tag handed over in negated form ("!cn", from "geoip:!cn") is the tag cn.
func TestNegatedTag(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"ads": "a"}, map[string][4]byte{"CN": {1, 2, 3, 4}})
	f := writeSource(t, "v2fly", nil, nil)
	res, err := Merge(t.TempDir(), []string{"!ads"}, []string{"!cn", "!!CN"}, p, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := readIP(t, res.GeoIP); len(got) != 1 || got["CN"] == nil {
		t.Errorf("ip = %v", got)
	}
	if got := readSite(t, res.GeoSite); !reflect.DeepEqual(got, map[string][]string{"ads": {"a"}}) {
		t.Errorf("site = %v", got)
	}
	if res.From["geoip:cn"] != "roscomvpn" || res.From["geosite:ads"] != "roscomvpn" {
		t.Errorf("From = %v", res.From)
	}
}

func TestNeedsOnlyRequested(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"A": "a", "B": "b", "C": "c"}, map[string][4]byte{"X": {1, 0, 0, 0}, "Y": {2, 0, 0, 0}})
	f := writeSource(t, "v2fly", map[string]string{"D": "d", "E": "e"}, map[string][4]byte{"Z": {3, 0, 0, 0}})
	res, err := Merge(t.TempDir(), []string{"b", "d"}, []string{"y"}, p, f)
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for k := range readSite(t, res.GeoSite) {
		sites = append(sites, k)
	}
	sort.Strings(sites)
	if !reflect.DeepEqual(sites, []string{"B", "D"}) {
		t.Errorf("sites = %v", sites)
	}
	if got := readIP(t, res.GeoIP); len(got) != 1 || got["Y"] == nil {
		t.Errorf("ip = %v", got)
	}
}

func TestEmptyRequestWritesEmptyFiles(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"A": "a"}, nil)
	f := writeSource(t, "v2fly", nil, nil)
	res, err := Merge(t.TempDir(), nil, nil, p, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{res.GeoSite, res.GeoIP} {
		if _, err := os.Stat(path); err != nil {
			t.Error(err)
		}
	}
}

func TestAtomic(t *testing.T) {
	p := writeSource(t, "roscomvpn", map[string]string{"A": "new"}, map[string][4]byte{"X": {9, 9, 9, 9}})
	f := writeSource(t, "v2fly", nil, nil)
	dst := t.TempDir()
	old := writeSource(t, "old", map[string]string{"A": "old"}, map[string][4]byte{"X": {1, 1, 1, 1}})
	for _, pair := range [][2]string{{old.GeoSite, "geosite.dat"}, {old.GeoIP, "geoip.dat"}} {
		b, _ := os.ReadFile(pair[0])
		if err := os.WriteFile(filepath.Join(dst, pair[1]), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	injected := errors.New("injected failure")
	afterTemps = func() error { return injected }
	defer func() { afterTemps = nil }()
	if _, err := Merge(dst, []string{"a"}, []string{"x"}, p, f); !errors.Is(err, injected) {
		t.Fatalf("err = %v, want injected", err)
	}
	if got := readSite(t, filepath.Join(dst, "geosite.dat")); !reflect.DeepEqual(got, map[string][]string{"A": {"old"}}) {
		t.Errorf("geosite changed: %v", got)
	}
	if got := readIP(t, filepath.Join(dst, "geoip.dat")); !reflect.DeepEqual(got, map[string][]byte{"X": {1, 1, 1, 1}}) {
		t.Errorf("geoip changed: %v", got)
	}
	ents, _ := os.ReadDir(dst)
	for _, e := range ents {
		if strings.Contains(e.Name(), "tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(ents) != 2 {
		t.Errorf("dir has %d entries, want 2", len(ents))
	}

	// A missing tag also leaves the existing output alone.
	afterTemps = nil
	if _, err := Merge(dst, []string{"nope"}, nil, p, f); err == nil {
		t.Fatal("want error")
	}
	if got := readSite(t, filepath.Join(dst, "geosite.dat")); !reflect.DeepEqual(got, map[string][]string{"A": {"old"}}) {
		t.Errorf("geosite changed after missing-tag failure: %v", got)
	}
}
