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
