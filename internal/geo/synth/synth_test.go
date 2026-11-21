package synth

import (
	"net/netip"
	"os"
	"testing"

	"github.com/xtls/xray-core/common/geodata"
	"google.golang.org/protobuf/proto"
)

func TestSynthRoundTrip(t *testing.T) {
	site := []string{"category-ru", "private"}
	ip := []string{"direct", "private", "ru"}
	sp, ipp, err := Write(t.TempDir(), site, ip)
	if err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(sp)
	if err != nil {
		t.Fatal(err)
	}
	var sl geodata.GeoSiteList
	if err := proto.Unmarshal(b, &sl); err != nil {
		t.Fatal(err)
	}
	if len(sl.Entry) != len(site) {
		t.Fatalf("site entries = %d, want %d", len(sl.Entry), len(site))
	}
	wantSite := map[string]string{"CATEGORY-RU": "category-ru.zatvor-test.invalid", "PRIVATE": "private.zatvor-test.invalid"}
	for _, e := range sl.Entry {
		d, ok := wantSite[e.Code]
		if !ok || len(e.Domain) != 1 || e.Domain[0].Value != d {
			t.Errorf("bad site entry %q: %v", e.Code, e.Domain)
		}
		delete(wantSite, e.Code)
	}
	if len(wantSite) != 0 {
		t.Errorf("missing site tags: %v", wantSite)
	}

	b, err = os.ReadFile(ipp)
	if err != nil {
		t.Fatal(err)
	}
	var il geodata.GeoIPList
	if err := proto.Unmarshal(b, &il); err != nil {
		t.Fatal(err)
	}
	if len(il.Entry) != len(ip) {
		t.Fatalf("ip entries = %d, want %d", len(il.Entry), len(ip))
	}
	seen := map[string]bool{}
	bench := netip.MustParsePrefix("198.18.0.0/15")
	for _, e := range il.Entry {
		if seen[e.Code] {
			t.Errorf("duplicate %q", e.Code)
		}
		seen[e.Code] = true
		if len(e.Cidr) != 1 || e.Cidr[0].Prefix != 32 {
			t.Fatalf("bad cidr for %q: %v", e.Code, e.Cidr)
		}
		a, ok := netip.AddrFromSlice(e.Cidr[0].Ip)
		if !ok || !bench.Contains(a) {
			t.Errorf("%q: %v not in 198.18.0.0/15", e.Code, e.Cidr[0].Ip)
		}
	}
	for _, c := range []string{"DIRECT", "PRIVATE", "RU"} {
		if !seen[c] {
			t.Errorf("missing ip tag %s", c)
		}
	}
}
