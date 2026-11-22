package happ

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

const fixtures = "../../testdata/fixtures/"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func linkProfile(t *testing.T) Profile {
	t.Helper()
	p, err := DecodeLink(string(readFixture(t, "happ-routing-link.txt")))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// semantic parses and re-marshals so only object key order is normalised away
// by comparing through any-trees.
func toTree(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case *ojson.Object:
		m := map[string]any{}
		for i := 0; i < x.Len(); i++ {
			k, val := x.At(i)
			m[k] = toTree(t, val)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i := range x {
			s[i] = toTree(t, x[i])
		}
		return s
	default:
		return v
	}
}

func marshalTree(t *testing.T, o *ojson.Object) any {
	t.Helper()
	b, err := ojson.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ojson.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return toTree(t, v)
}

func TestTranslateMatchesLegacyFixture(t *testing.T) {
	routing, dns, err := Translate(linkProfile(t), "proxy", "direct", "block")
	if err != nil {
		t.Fatal(err)
	}
	v, err := ojson.Parse(readFixture(t, "xray-hy2-dns-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := v.(*ojson.Object)
	wr, _ := want.Get("routing")
	wd, _ := want.Get("dns")
	if got, exp := marshalTree(t, routing), toTree(t, wr); !reflect.DeepEqual(got, exp) {
		t.Errorf("routing mismatch:\n got %v\nwant %v", got, exp)
	}
	if got, exp := marshalTree(t, dns), toTree(t, wd); !reflect.DeepEqual(got, exp) {
		t.Errorf("dns mismatch:\n got %v\nwant %v", got, exp)
	}
}

func TestDecodeLinkEqualsJSON(t *testing.T) {
	fromJSON, err := DecodeJSON(readFixture(t, "happ-routing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fromLink := linkProfile(t); !reflect.DeepEqual(fromLink, fromJSON) {
		t.Errorf("link and JSON profiles differ:\n%+v\n%+v", fromLink, fromJSON)
	}
	if fromJSON.Name != "Example RU-direct" || fromJSON.DnsHosts["lknpd.nalog.ru"] != "213.24.64.181" {
		t.Errorf("fields not decoded: %+v", fromJSON)
	}
}

func TestDecodeLinkVariants(t *testing.T) {
	want := linkProfile(t)
	raw := strings.TrimSpace(string(readFixture(t, "happ-routing-link.txt")))
	payload := strings.TrimPrefix(raw, "happ://routing/onadd/")
	std, _ := base64.StdEncoding.DecodeString(payload)
	for name, link := range map[string]string{
		"add":      "happ://routing/add/" + payload,
		"url-safe": "happ://routing/onadd/" + base64.URLEncoding.EncodeToString(std),
		"raw-url":  "happ://routing/onadd/" + base64.RawURLEncoding.EncodeToString(std),
		"raw-std":  "happ://routing/onadd/" + base64.RawStdEncoding.EncodeToString(std),
		"newlines": "  " + raw + "\r\n",
		"inner-nl": "happ://routing/onadd/" + payload[:100] + "\n" + payload[100:],
	} {
		got, err := DecodeLink(link)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: profile differs", name)
		}
	}
}

func TestDecodeLinkErrors(t *testing.T) {
	for _, in := range []string{
		"", "happ://routing/off", "happ://add/https://example.com/sub",
		"https://example.com/", "happ://routing/onadd/", "happ://routing/onadd/!!!",
		"happ://routing/onadd/" + base64.StdEncoding.EncodeToString([]byte("not json")),
		"happ://routing/onadd/" + base64.StdEncoding.EncodeToString([]byte("[1]")),
	} {
		if _, err := DecodeLink(in); err == nil {
			t.Errorf("DecodeLink(%q): want error", in)
		}
	}
	if _, err := DecodeJSON([]byte(`{"DirectSites":"x"}`)); err == nil {
		t.Error("wrong field type: want error")
	}
}

func ruleTags(t *testing.T, routing *ojson.Object) []string {
	t.Helper()
	rules, _ := routing.Get("rules")
	var out []string
	for _, r := range rules.([]any) {
		o := r.(*ojson.Object)
		tag, _ := o.Get("outboundTag")
		for _, k := range []string{"domain", "ip", "network"} {
			if _, ok := o.Get(k); ok {
				out = append(out, tag.(string)+":"+k)
				break
			}
		}
	}
	return out
}

func TestRouteOrder(t *testing.T) {
	p := linkProfile(t)
	p.ProxyIp = []string{"1.2.3.0/24"}
	p.BlockIp = []string{"geoip:cn"}
	run := func(order string) []string {
		p.RouteOrder = order
		r, _, err := Translate(p, "proxy", "direct", "block")
		if err != nil {
			t.Fatal(err)
		}
		return ruleTags(t, r)
	}
	a := run("block-proxy-direct")
	b := run("proxy-direct-block")
	wantA := []string{"block:domain", "block:ip", "proxy:domain", "proxy:ip", "direct:domain", "direct:ip", "proxy:network"}
	wantB := []string{"proxy:domain", "proxy:ip", "direct:domain", "direct:ip", "block:domain", "block:ip", "proxy:network"}
	if !reflect.DeepEqual(a, wantA) {
		t.Errorf("block-proxy-direct: %v", a)
	}
	if !reflect.DeepEqual(b, wantB) {
		t.Errorf("proxy-direct-block: %v", b)
	}
	if def := run(""); def[0] != "block:domain" || def[2] != "direct:domain" {
		t.Errorf("default order should be block-direct-proxy: %v", def)
	}
}

func TestGlobalProxyFalse(t *testing.T) {
	p := linkProfile(t)
	p.GlobalProxy = "false"
	r, _, err := Translate(p, "proxy", "direct", "block")
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := r.Get("rules")
	last := rules.([]any)[len(rules.([]any))-1].(*ojson.Object)
	if tag, _ := last.Get("outboundTag"); tag != "direct" {
		t.Errorf("final rule outboundTag = %v, want direct", tag)
	}
}

func TestCustomTagsAndDNSDefaults(t *testing.T) {
	p := Profile{RemoteDNSType: "DoU"}
	r, d, err := Translate(p, "p", "d", "b")
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleTags(t, r); !reflect.DeepEqual(got, []string{"p:network"}) {
		t.Errorf("empty profile rules: %v", got)
	}
	if ds, _ := r.Get("domainStrategy"); ds != "IPIfNonMatch" {
		t.Errorf("domainStrategy = %v", ds)
	}
	servers, _ := d.Get("servers")
	if n := len(servers.([]any)); n != 1 {
		t.Errorf("servers = %d, want only remote", n)
	}
	if _, ok := d.Get("hosts"); ok {
		t.Error("hosts should be absent when empty")
	}
}

func TestGeoURLs(t *testing.T) {
	ip, site := GeoURLs(linkProfile(t))
	if !strings.HasSuffix(ip, "/geoip.dat") || !strings.HasSuffix(site, "/geosite.dat") {
		t.Errorf("GeoURLs = %q, %q", ip, site)
	}
}
