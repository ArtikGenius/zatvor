package link

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

func outboundJSON(t *testing.T, l string) map[string]any {
	t.Helper()
	n, err := Parse(l)
	if err != nil {
		t.Fatal(err)
	}
	o, err := Outbound(n, "proxy")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ojson.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOutboundHy2MatchesFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fixtures/xray-hy2-dns-finalmask.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	for _, o := range cfg.Outbounds {
		if o["tag"] == "proxy" {
			want = o
		}
	}
	if want == nil {
		t.Fatal("no proxy outbound in fixture")
	}
	// Share links carry neither the noise item nor quicParams.
	fm := want["streamSettings"].(map[string]any)["finalmask"].(map[string]any)
	var kept []any
	for _, it := range fm["udp"].([]any) {
		if it.(map[string]any)["type"] != "noise" {
			kept = append(kept, it)
		}
	}
	fm["udp"] = kept
	delete(fm, "quicParams")

	got := outboundJSON(t, "hysteria2://REPLACE_ME_hysteria2_auth@hy2.example.com:8443/?obfs=salamander&obfs-password=REPLACE_ME_salamander_password&sni=hy2.example.com#fixture")
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("outbound differs from fixture:\n got %s\nwant %s", g, w)
	}
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func TestOutboundRealityVision(t *testing.T) {
	m := outboundJSON(t, "vless://REPLACE_ME_uuid@reality.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.org&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=ab12&spx=%2F&type=tcp#r")
	if m["protocol"] != "vless" || m["tag"] != "proxy" {
		t.Fatalf("bad header: %v", m)
	}
	user := dig(m, "settings", "vnext").([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["id"] != "REPLACE_ME_uuid" || user["flow"] != "xtls-rprx-vision" || user["encryption"] != "none" {
		t.Fatalf("user = %v", user)
	}
	if dig(m, "streamSettings", "security") != "reality" || dig(m, "streamSettings", "network") != "tcp" {
		t.Fatalf("stream = %v", m["streamSettings"])
	}
	want := map[string]any{"serverName": "www.example.org", "fingerprint": "chrome", "publicKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "shortId": "ab12", "spiderX": "/"}
	if !reflect.DeepEqual(dig(m, "streamSettings", "realitySettings"), want) {
		t.Fatalf("reality = %v", dig(m, "streamSettings", "realitySettings"))
	}
}

func TestOutboundXHTTPExtraIsObject(t *testing.T) {
	m := outboundJSON(t, `vless://REPLACE_ME_uuid@x.example.com:443?security=tls&sni=x.example.com&type=xhttp&path=%2Fx&mode=auto&extra=%7B%22xmux%22%3A%7B%22maxConcurrency%22%3A%2216-32%22%7D%7D#x`)
	x := dig(m, "streamSettings", "xhttpSettings").(map[string]any)
	if x["path"] != "/x" || x["mode"] != "auto" {
		t.Fatalf("xhttp = %v", x)
	}
	if dig(x, "extra", "xmux", "maxConcurrency") != "16-32" {
		t.Fatalf("extra not merged as object: %v", x["extra"])
	}
}

func TestOutboundWSGRPCNone(t *testing.T) {
	m := outboundJSON(t, "vless://REPLACE_ME_uuid@ws.example.com:8443?security=tls&sni=ws.example.com&alpn=h2%2Chttp%2F1.1&type=ws&path=%2Fws&host=h.example.com#w")
	if dig(m, "streamSettings", "wsSettings", "path") != "/ws" || dig(m, "streamSettings", "wsSettings", "host") != "h.example.com" {
		t.Fatalf("ws = %v", m["streamSettings"])
	}
	tls := dig(m, "streamSettings", "tlsSettings").(map[string]any)
	if !reflect.DeepEqual(tls["alpn"], []any{"h2", "http/1.1"}) || tls["allowInsecure"] != nil {
		t.Fatalf("tls = %v", tls)
	}
	m = outboundJSON(t, "vless://REPLACE_ME_uuid@g.example.com:443?security=tls&type=grpc&serviceName=svc&mode=multi#g")
	if dig(m, "streamSettings", "grpcSettings", "serviceName") != "svc" || dig(m, "streamSettings", "grpcSettings", "multiMode") != true {
		t.Fatalf("grpc = %v", m["streamSettings"])
	}
	m = outboundJSON(t, "vless://REPLACE_ME_uuid@p.example.com:80?security=none#p")
	if dig(m, "streamSettings", "security") != "none" || dig(m, "streamSettings", "tlsSettings") != nil {
		t.Fatalf("none = %v", m["streamSettings"])
	}
}

func TestOutboundOtherProtocols(t *testing.T) {
	// {"v":"2","ps":"x","add":"vm.example.com","port":"443","id":"REPLACE_ME_uuid","aid":"2","net":"ws","path":"/ws","tls":"tls"}
	m := outboundJSON(t, "vmess://eyJ2IjoiMiIsInBzIjoieCIsImFkZCI6InZtLmV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiUkVQTEFDRV9NRV91dWlkIiwiYWlkIjoiMiIsIm5ldCI6IndzIiwicGF0aCI6Ii93cyIsInRscyI6InRscyJ9")
	u := dig(m, "settings", "vnext").([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if m["protocol"] != "vmess" || u["alterId"] != float64(2) || u["security"] != "auto" {
		t.Fatalf("vmess = %v", m)
	}
	if dig(m, "streamSettings", "network") != "ws" || dig(m, "streamSettings", "security") != "tls" {
		t.Fatalf("vmess stream = %v", m["streamSettings"])
	}
	m = outboundJSON(t, "trojan://REPLACE_ME_password@t.example.com:443?sni=t.example.com#t")
	s := dig(m, "settings", "servers").([]any)[0].(map[string]any)
	if m["protocol"] != "trojan" || s["password"] != "REPLACE_ME_password" || dig(m, "streamSettings", "security") != "tls" {
		t.Fatalf("trojan = %v", m)
	}
	m = outboundJSON(t, "ss://YWVzLTI1Ni1nY206UkVQTEFDRV9NRV9wYXNz@ss.example.com:8388#s")
	s = dig(m, "settings", "servers").([]any)[0].(map[string]any)
	if m["protocol"] != "shadowsocks" || s["method"] != "aes-256-gcm" || s["password"] != "REPLACE_ME_pass" || s["port"] != float64(8388) {
		t.Fatalf("ss = %v", m)
	}
	m = outboundJSON(t, "socks://REPLACE_ME_user:REPLACE_ME_pass@s.example.com:1080#s")
	s = dig(m, "settings", "servers").([]any)[0].(map[string]any)
	us := s["users"].([]any)[0].(map[string]any)
	if m["protocol"] != "socks" || us["user"] != "REPLACE_ME_user" || us["pass"] != "REPLACE_ME_pass" {
		t.Fatalf("socks = %v", m)
	}
}

func TestOutboundErrors(t *testing.T) {
	for _, c := range []struct {
		link string
		want error
	}{
		{"ss://YWVzLTI1Ni1nY206UkVQTEFDRV9NRV9wYXNz@ss.example.com:8388/?plugin=obfs-local%3Bobfs%3Dhttp#p", ErrUnsupported},
		{"vless://REPLACE_ME@h.example.com:443?security=tls&type=kcp#k", ErrUnsupported},
		{"vless://REPLACE_ME@h.example.com:443?security=tls&type=xhttp&extra=%5B1%5D#bad-extra", ErrMalformed},
		{"vless://REPLACE_ME@h.example.com:443?security=tls&type=xhttp&extra=%7B#bad-json", ErrMalformed},
	} {
		n, err := Parse(c.link)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Outbound(n, "proxy"); !errors.Is(err, c.want) {
			t.Errorf("%.20s: err = %v, want %v", c.link, err, c.want)
		} else if strings.Contains(err.Error(), "REPLACE_ME") {
			t.Errorf("error leaks secret: %v", err)
		}
	}
}

func TestOutboundInsecureTLS(t *testing.T) {
	for _, l := range []string{
		"vless://REPLACE_ME@h.example.com:443?security=tls&allowInsecure=1#i",
		"trojan://REPLACE_ME@h.example.com:443?insecure=1#i",
		"hysteria2://REPLACE_ME@h.example.com:443?insecure=1#i",
	} {
		n, err := Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Outbound(n, "proxy")
		if !errors.Is(err, ErrInsecureTLS) || strings.Contains(err.Error(), "REPLACE_ME") {
			t.Errorf("%.12s: err = %v, want ErrInsecureTLS", l, err)
		}
	}
	// A pin wins over the insecure flag; allowInsecure is never emitted.
	m := outboundJSON(t, "hysteria2://REPLACE_ME@h.example.com:443?insecure=1&pinSHA256=AA%3ABB%2CCC#p")
	tls := dig(m, "streamSettings", "tlsSettings").(map[string]any)
	if tls["pinnedPeerCertSha256"] != "aabb,cc" || tls["allowInsecure"] != nil {
		t.Fatalf("tls = %v", tls)
	}
}
