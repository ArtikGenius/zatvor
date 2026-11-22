package geo

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNeededTagsLegacyFixture(t *testing.T) {
	site, ip, err := NeededTags(fixture(t, "xray-hy2-dns-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantSite := []string{
		"apple", "category-ads", "category-ru", "epicgames", "escapefromtarkov",
		"faceit", "github", "google-play", "microsoft", "pinterest", "private",
		"riot", "steam", "telegram", "torrent", "twitch", "twitch-ads",
		"whitelist", "win-spy", "youtube",
	}
	wantIP := []string{"direct", "private"}
	if !reflect.DeepEqual(site, wantSite) {
		t.Errorf("site = %v, want %v", site, wantSite)
	}
	if !reflect.DeepEqual(ip, wantIP) {
		t.Errorf("ip = %v, want %v", ip, wantIP)
	}
}

func TestNeededTagsFinalmaskFixture(t *testing.T) {
	site, ip, err := NeededTags(fixture(t, "xray-hy2-dns-finalmask.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"category-ru"}; !reflect.DeepEqual(site, want) {
		t.Errorf("site = %v, want %v", site, want)
	}
	if want := []string{"private", "ru"}; !reflect.DeepEqual(ip, want) {
		t.Errorf("ip = %v, want %v", ip, want)
	}
}

func TestNeededTagsExtRef(t *testing.T) {
	cfg := `{"routing":{"rules":[{"domain":["ext:geosite.dat:foo"]}]}}`
	if _, _, err := NeededTags([]byte(cfg)); !errors.Is(err, ErrExtRef) {
		t.Fatalf("err = %v, want ErrExtRef", err)
	}
}

func TestNeededTagsBadJSON(t *testing.T) {
	if _, _, err := NeededTags([]byte("{")); err == nil {
		t.Fatal("want error")
	}
}

// Xray reverses an IP rule on a leading "!" (any number) and on "!" before
// the code (common/geodata/rule_parser.go cutReversePrefix).
func TestNeededTagsNegated(t *testing.T) {
	cfg := `{"routing":{"rules":[
	  {"ip":["!geoip:cn","geoip:!ru","!!geoip:!!by","!geoip:private"]},
	  {"domain":["!geosite:ads"]}
	]},"dns":{"servers":[{"address":"1.1.1.1","expectIPs":["geoip:!ir"]}]}}`
	site, ip, err := NeededTags([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ads"}; !reflect.DeepEqual(site, want) {
		t.Errorf("site = %v, want %v", site, want)
	}
	if want := []string{"by", "cn", "ir", "private", "ru"}; !reflect.DeepEqual(ip, want) {
		t.Errorf("ip = %v, want %v", ip, want)
	}
	if _, _, err := NeededTags([]byte(`{"routing":{"rules":[{"ip":["!ext:geoip.dat:cn"]}]}}`)); !errors.Is(err, ErrExtRef) {
		t.Errorf("negated ext: err = %v, want ErrExtRef", err)
	}
}

// Every config field Xray reads geo rules from (infra/conf router.go and
// dns.go) is collected.
func TestNeededTagsAllFields(t *testing.T) {
	cfg := `{"routing":{"rules":[
	  {"domains":["geosite:a"]},{"source":["geoip:s1"]},{"sourceIP":["geoip:s2"]},{"localIP":["geoip:l"]}
	]},"dns":{"hosts":{"geosite:h":"127.0.0.1","example.com":"1.2.3.4"},"servers":[
	  {"address":"1.1.1.1","expectedIPs":["geoip:e"],"unexpectedIPs":["geoip:u"]}
	]}}`
	site, ip, err := NeededTags([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "h"}; !reflect.DeepEqual(site, want) {
		t.Errorf("site = %v, want %v", site, want)
	}
	if want := []string{"e", "l", "s1", "s2", "u"}; !reflect.DeepEqual(ip, want) {
		t.Errorf("ip = %v, want %v", ip, want)
	}
}
