package gen

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/happ"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/profile"
)

var update = flag.Bool("update", false, "rewrite golden files")

const realityLink = "vless://REPLACE_ME_uuid@reality.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.example.org&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=ab12&spx=%2F&type=tcp#r"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseObj(t *testing.T, data []byte) *ojson.Object {
	t.Helper()
	v, err := ojson.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*ojson.Object)
}

func nodeProfile(t *testing.T, raw string, src profile.Source) profile.Profile {
	t.Helper()
	n, err := link.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return profile.Profile{ID: "p", Name: "p", Node: &n, RoutingSource: src}
}

func configProfile(t *testing.T, fixture string, src profile.Source) profile.Profile {
	t.Helper()
	return profile.Profile{ID: "c", Name: "c", Config: parseObj(t, readFixture(t, fixture)), RoutingSource: src}
}

func baseInput(p profile.Profile) Input {
	return Input{
		Profile:   p,
		Ports:     Ports{Mixed: 20001, Probe: 20002, DevTools: 20003, API: 20004},
		ProbeUser: "probe",
		ProbePass: "REPLACE_ME_probe",
	}
}

func mustBuild(t *testing.T, in Input) ([]byte, Report) {
	t.Helper()
	out, rep, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return out, rep
}

// golden compares out with testdata/golden/gen/<name> as ordered JSON.
func golden(t *testing.T, name string, out []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", "gen", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ojson.Marshal(parseObj(t, want))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ojson.Marshal(parseObj(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("%s differs from output:\n%s", name, out)
	}
}

// dig walks a generic decoded JSON value.
func dig(v any, keys ...any) any {
	for _, k := range keys {
		switch kk := k.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[kk]
		case int:
			l, ok := v.([]any)
			if !ok || kk >= len(l) {
				return nil
			}
			v = l[kk]
		}
	}
	return v
}

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	return m
}

func TestBuildLinkAllProxy(t *testing.T) {
	src := profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}
	out, rep := mustBuild(t, baseInput(nodeProfile(t, realityLink, src)))
	golden(t, "link-all-proxy.json", out)
	if rep.ProxyTag != "proxy" || len(rep.Warnings) != 0 || len(rep.ReplacedInbounds) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestBuildLinkAllExceptRU(t *testing.T) {
	src := profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllExceptRU}
	out, _ := mustBuild(t, baseInput(nodeProfile(t, realityLink, src)))
	golden(t, "link-all-except-ru.json", out)
}

func TestBuildSelectedOnly(t *testing.T) {
	src := profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetSelectedOnly}
	in := baseInput(nodeProfile(t, realityLink, src))
	in.SelectedSites = []string{"domain:example.com"}
	out, _ := mustBuild(t, in)
	golden(t, "selected-only.json", out)
}

func TestBuildProviderJSON(t *testing.T) {
	in := baseInput(configProfile(t, "xray-hy2-dns-finalmask.json", profile.Source{Kind: profile.SourceProvider}))
	out, rep := mustBuild(t, in)
	golden(t, "provider-finalmask.json", out)

	// Review Focus 4: the provider's single inbound tag stays on our mixed
	// inbound, so provider rules with inboundTag keep matching.
	m := decode(t, out)
	if dig(m, "inbounds", 0, "tag") != "socks" {
		t.Fatalf("mixed tag = %v", dig(m, "inbounds", 0, "tag"))
	}
	if len(rep.ReplacedInbounds) != 1 || rep.ReplacedInbounds[0] != "socks" || rep.ProxyTag != "proxy" {
		t.Fatalf("report = %+v", rep)
	}

	// dns is byte-identical to the provider's.
	orig := parseObj(t, readFixture(t, "xray-hy2-dns-finalmask.json"))
	gotDNS, _ := parseObj(t, out).Get("dns")
	wantDNS, _ := orig.Get("dns")
	a, _ := ojson.Marshal(gotDNS)
	b, _ := ojson.Marshal(wantDNS)
	if !bytes.Equal(a, b) {
		t.Fatalf("dns changed:\n%s\n%s", a, b)
	}

	// Provider rules follow ours, in order and content.
	gotRT, _ := parseObj(t, out).Get("routing")
	gotRules, _ := gotRT.(*ojson.Object).Get("rules")
	origRT, _ := orig.Get("routing")
	origRules, _ := origRT.(*ojson.Object).Get("rules")
	gr, or := gotRules.([]any), origRules.([]any)
	tail := gr[len(gr)-len(or):]
	a, _ = ojson.Marshal(tail)
	b, _ = ojson.Marshal(or)
	if !bytes.Equal(a, b) {
		t.Fatalf("provider rules changed:\n%s\n%s", a, b)
	}
	if dig(m, "routing", "domainStrategy") != "IPIfNonMatch" {
		t.Fatalf("other routing keys lost: %v", m["routing"])
	}
}

func TestBuildHappSource(t *testing.T) {
	hp, err := happ.DecodeLink(strings.TrimSpace(string(readFixture(t, "happ-routing-link.txt"))))
	if err != nil {
		t.Fatal(err)
	}
	p := configProfile(t, "xray-hy2-dns-legacy.json", profile.Source{Kind: profile.SourceHapp, Happ: &hp})
	out, rep := mustBuild(t, baseInput(p))
	golden(t, "happ-legacy.json", out)

	wantRT, wantDNS, err := happ.Translate(hp, rep.ProxyTag, "direct", "block")
	if err != nil {
		t.Fatal(err)
	}
	root := parseObj(t, out)
	gotDNS, _ := root.Get("dns")
	a, _ := ojson.Marshal(gotDNS)
	b, _ := ojson.Marshal(wantDNS)
	if !bytes.Equal(a, b) {
		t.Fatalf("dns differs from happ.Translate:\n%s\n%s", a, b)
	}
	// Translate's rules are the tail of ours.
	rt, _ := root.Get("routing")
	got, _ := rt.(*ojson.Object).Get("rules")
	want, _ := wantRT.Get("rules")
	g, w := got.([]any), want.([]any)
	a, _ = ojson.Marshal(g[len(g)-len(w):])
	b, _ = ojson.Marshal(w)
	if !bytes.Equal(a, b) {
		t.Fatalf("rules differ from happ.Translate:\n%s\n%s", a, b)
	}
	_, hasFake := root.Get("fakedns")
	if (happ.FakeDNS(hp) != nil) != hasFake {
		t.Fatalf("fakedns presence = %v", hasFake)
	}
}

func TestHappFakeDNSAtRoot(t *testing.T) {
	hp := happ.Profile{FakeDNS: "true", ProxySites: []string{"domain:example.com"}}
	p := nodeProfile(t, realityLink, profile.Source{Kind: profile.SourceHapp, Happ: &hp})
	out, _ := mustBuild(t, baseInput(p))
	m := decode(t, out)
	if dig(m, "fakedns", "ipPool") != "198.18.0.0/15" {
		t.Fatalf("fakedns = %v", m["fakedns"])
	}
	if !strings.Contains(string(out), `"fakedns"`) || dig(m, "inbounds", 0, "sniffing", "destOverride", 3) != "fakedns" {
		t.Fatalf("sniffing lacks fakedns: %v", dig(m, "inbounds", 0, "sniffing"))
	}
}

func TestLinkProviderWithoutHappWarns(t *testing.T) {
	p := nodeProfile(t, realityLink, profile.Source{Kind: profile.SourceProvider})
	out, rep := mustBuild(t, baseInput(p))
	if len(rep.Warnings) != 1 {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	rules := dig(decode(t, out), "routing", "rules").([]any)
	last := rules[len(rules)-1]
	if dig(last, "network") != "tcp,udp" || dig(last, "outboundTag") != "proxy" {
		t.Fatalf("not all-proxy: %v", last)
	}
	if _, ok := decode(t, out)["dns"]; ok {
		t.Fatal("preset emitted dns")
	}
}

func TestLinkProviderWithHappTranslates(t *testing.T) {
	hp := happ.Profile{GlobalProxy: "false", DirectSites: []string{"domain:ru"}}
	p := nodeProfile(t, realityLink, profile.Source{Kind: profile.SourceProvider, Happ: &hp})
	out, rep := mustBuild(t, baseInput(p))
	if len(rep.Warnings) != 0 {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	m := decode(t, out)
	rules := m["routing"].(map[string]any)["rules"].([]any)
	if dig(rules[len(rules)-1], "outboundTag") != "direct" || m["dns"] == nil {
		t.Fatalf("happ not applied: %v", m["routing"])
	}
}

func ruleList(t *testing.T, out []byte) []any {
	t.Helper()
	return dig(decode(t, out), "routing", "rules").([]any)
}

func marshalAny(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRuleLayers(t *testing.T) {
	src := profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}
	in := baseInput(nodeProfile(t, realityLink, src))
	in.DevTools = true
	in.UserBlocks = []string{"domain:bad.example"}
	out, rep := mustBuild(t, in)
	if rep.ProxyTag != "proxy" {
		t.Fatalf("proxy tag = %q", rep.ProxyTag)
	}
	ai := make([]any, len(AIDomains))
	for i, d := range AIDomains {
		ai[i] = d
	}
	want := []any{
		map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"},
		map[string]any{"type": "field", "inboundTag": []any{"probe-in"}, "outboundTag": "proxy"},
		map[string]any{"type": "field", "domain": []any{"domain:bad.example"}, "outboundTag": "block"},
		map[string]any{"type": "field", "inboundTag": []any{"devtools-in"}, "outboundTag": "proxy"},
		map[string]any{"type": "field", "domain": ai, "outboundTag": "proxy"},
		map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": "proxy"}, // source default
	}
	if got := ruleList(t, out); marshalAny(t, got) != marshalAny(t, want) {
		t.Fatalf("rules:\n%s\nwant:\n%s", marshalAny(t, got), marshalAny(t, want))
	}
}

func TestProbeRuleTargetsBalancer(t *testing.T) {
	// A provider whose first catch-all points at a balancer: the probe rule
	// targets the balancer.
	cfg := `{"outbounds":[{"tag":"a","protocol":"vless"},{"tag":"b","protocol":"vless"}],
	"routing":{"balancers":[{"tag":"bal","selector":["a","b"]}],
	"rules":[{"type":"field","network":"tcp,udp","balancerTag":"bal"}]}}`
	p := profile.Profile{Config: parseObj(t, []byte(cfg))}
	out, rep := mustBuild(t, baseInput(p))
	if rep.ProxyTag != "bal" {
		t.Fatalf("proxy tag = %q", rep.ProxyTag)
	}
	r := ruleList(t, out)[1]
	if dig(r, "balancerTag") != "bal" || dig(r, "outboundTag") != nil {
		t.Fatalf("probe rule = %v", r)
	}
	if dig(decode(t, out), "routing", "balancers", 0, "tag") != "bal" {
		t.Fatal("balancers lost")
	}
}

func TestProxyTagFirstCatchAllWins(t *testing.T) {
	cfg := `{"outbounds":[{"tag":"a","protocol":"vless"},{"tag":"b","protocol":"vless"},{"tag":"c","protocol":"vless"}],
	"routing":{"rules":[{"type":"field","domain":["x"],"outboundTag":"c"},
	{"type":"field","network":"tcp,udp","outboundTag":"b"},
	{"type":"field","network":"tcp,udp","outboundTag":"c"}]}}`
	out, rep := mustBuild(t, baseInput(profile.Profile{Config: parseObj(t, []byte(cfg))}))
	if rep.ProxyTag != "b" || dig(ruleList(t, out)[1], "outboundTag") != "b" {
		t.Fatalf("proxy tag = %q", rep.ProxyTag)
	}
}

func TestProxyTagFallsBackToFirstOutbound(t *testing.T) {
	cfg := `{"outbounds":[{"tag":"first","protocol":"vless"}],"routing":{"rules":[{"type":"field","domain":["x"],"outboundTag":"other"}]}}`
	_, rep := mustBuild(t, baseInput(profile.Profile{Config: parseObj(t, []byte(cfg))}))
	if rep.ProxyTag != "first" {
		t.Fatalf("proxy tag = %q", rep.ProxyTag)
	}
}

func TestBootstrapRule(t *testing.T) {
	src := profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllExceptRU}
	in := baseInput(nodeProfile(t, realityLink, src))
	in.UserBlocks = []string{"domain:bad.example"}
	plain, _ := mustBuild(t, in)
	in.BootstrapAllProxy = true
	out, _ := mustBuild(t, in)
	a, b := ruleList(t, plain), ruleList(t, out)
	if len(b) != len(a)+1 {
		t.Fatalf("want one extra rule, got %d vs %d", len(b), len(a))
	}
	// After internal (2) and block (1) rules, before the source's.
	if marshalAny(t, b[3]) != `{"network":"tcp,udp","outboundTag":"proxy","type":"field"}` {
		t.Fatalf("bootstrap rule = %s", marshalAny(t, b[3]))
	}
	if marshalAny(t, b[:3]) != marshalAny(t, a[:3]) || marshalAny(t, b[4:]) != marshalAny(t, a[3:]) {
		t.Fatal("other rules moved")
	}
}

func TestInbounds(t *testing.T) {
	src := profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}
	in := baseInput(nodeProfile(t, realityLink, src))
	in.DevTools = true
	out, _ := mustBuild(t, in)
	m := decode(t, out)
	ib := m["inbounds"].([]any)
	if len(ib) != 4 {
		t.Fatalf("inbounds = %d", len(ib))
	}
	for _, x := range ib {
		if dig(x, "listen") != "127.0.0.1" {
			t.Fatalf("non-loopback inbound: %v", x)
		}
	}
	checks := []struct {
		i       int
		tag, pr string
		port    float64
	}{{0, "mixed-in", "mixed", 20001}, {1, "probe-in", "socks", 20002}, {2, "devtools-in", "mixed", 20003}, {3, "api", "dokodemo-door", 20004}}
	for _, c := range checks {
		if dig(ib, c.i, "tag") != c.tag || dig(ib, c.i, "protocol") != c.pr || dig(ib, c.i, "port") != c.port {
			t.Fatalf("inbound %d = %v", c.i, ib[c.i])
		}
	}
	if dig(ib, 0, "sniffing", "enabled") != true {
		t.Fatal("sniffing off on mixed")
	}
	if dig(ib, 1, "settings", "auth") != "password" || dig(ib, 1, "settings", "accounts", 0, "user") != "probe" ||
		dig(ib, 1, "settings", "accounts", 0, "pass") != "REPLACE_ME_probe" {
		t.Fatalf("probe settings = %v", dig(ib, 1, "settings"))
	}
	if marshalAny(t, dig(m, "api", "services")) != `["HandlerService","StatsService","LoggerService"]` || dig(m, "api", "tag") != "api" {
		t.Fatalf("api = %v", m["api"])
	}
	if strings.Contains(string(out), "0.0.0.0") || strings.Contains(string(out), `"::"`) {
		t.Fatal("wildcard listener")
	}

	// Dev-tools inbound only when the feature is on.
	in.DevTools = false
	out, _ = mustBuild(t, in)
	if n := len(decode(t, out)["inbounds"].([]any)); n != 3 {
		t.Fatalf("inbounds without dev tools = %d", n)
	}
}

func TestSerializerEscapes(t *testing.T) {
	pw := "p\"a\\ss  тест # % \U0001F600"
	n := link.Node{Scheme: "trojan", Remark: pw, Address: "t.example.com", Port: 443,
		Params: map[string]string{"password": pw, "security": "tls", "sni": "t.example.com"}}
	p := profile.Profile{Node: &n, RoutingSource: profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}}
	in := baseInput(p)
	in.ProbePass = pw
	in.UserBlocks = []string{"domain:" + pw}
	out, _ := mustBuild(t, in)
	m := decode(t, out)
	if dig(m, "outbounds", 0, "settings", "servers", 0, "password") != pw {
		t.Fatalf("password mangled: %v", dig(m, "outbounds", 0, "settings"))
	}
	if dig(m, "inbounds", 1, "settings", "accounts", 0, "pass") != pw {
		t.Fatal("probe pass mangled")
	}
}

// Review Focus 3: awkward characters survive parse, generate and decode.
func TestAwkwardCharactersSurviveParse(t *testing.T) {
	pw := "q\"b\\s#h%25 пароль \U0001F680"
	remark := "Сервер \"1\" #a %b \U0001F680"
	raw := "trojan://" + url.PathEscape(pw) + "@t.example.com:443?security=tls&sni=t.example.com#" + url.PathEscape(remark)
	p := nodeProfile(t, raw, profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy})
	if p.Node.Params["password"] != pw || p.Node.Remark != remark {
		t.Fatalf("parse lost characters: %q %q", p.Node.Params["password"], p.Node.Remark)
	}
	out, _ := mustBuild(t, baseInput(p))
	if dig(decode(t, out), "outbounds", 0, "settings", "servers", 0, "password") != pw {
		t.Fatal("password mangled after build")
	}
}

func TestProviderInboundTags(t *testing.T) {
	cfg := func(inb, rules string) profile.Profile {
		s := `{"inbounds":` + inb + `,"outbounds":[{"tag":"proxy","protocol":"vless"}],
		"routing":{"rules":` + rules + `}}`
		return profile.Profile{Config: parseObj(t, []byte(s))}
	}
	t.Run("single tag kept across several inbounds", func(t *testing.T) {
		p := cfg(`[{"tag":"in","protocol":"socks","port":1},{"tag":"in","protocol":"http","port":2}]`,
			`[{"type":"field","inboundTag":["in"],"outboundTag":"direct"}]`)
		out, rep := mustBuild(t, baseInput(p))
		m := decode(t, out)
		if dig(m, "inbounds", 0, "tag") != "in" || len(rep.Warnings) != 0 {
			t.Fatalf("tag = %v warnings = %v", dig(m, "inbounds", 0, "tag"), rep.Warnings)
		}
		rules := m["routing"].(map[string]any)["rules"].([]any)
		if marshalAny(t, dig(rules[len(rules)-1], "inboundTag")) != `["in"]` {
			t.Fatalf("rule changed: %v", rules[len(rules)-1])
		}
	})
	t.Run("reserved provider tag is not kept", func(t *testing.T) {
		p := cfg(`[{"tag":"api","protocol":"socks","port":1}]`, `[]`)
		out, rep := mustBuild(t, baseInput(p))
		m := decode(t, out)
		if dig(m, "inbounds", 0, "tag") != "mixed-in" || len(rep.Warnings) != 0 {
			t.Fatalf("tag = %v warnings = %v", dig(m, "inbounds", 0, "tag"), rep.Warnings)
		}
	})
	t.Run("untagged provider inbounds use mixed-in", func(t *testing.T) {
		p := cfg(`[{"protocol":"socks","port":1}]`, `[]`)
		out, _ := mustBuild(t, baseInput(p))
		if dig(decode(t, out), "inbounds", 0, "tag") != "mixed-in" {
			t.Fatal("tag")
		}
	})
}

func TestProviderMultipleInboundTags(t *testing.T) {
	rules := `[{"type":"field","inboundTag":["a","b","other"],"outboundTag":"direct"},{"type":"field","inboundTag":["c"],"outboundTag":"proxy"}]`
	cfg := `{"inbounds":[{"tag":"a","protocol":"vmess","port":1},{"tag":"b","protocol":"http","port":2},{"tag":"c","protocol":"socks","port":3},{"protocol":"http","port":4}],
	"outbounds":[{"tag":"proxy","protocol":"vless"}],"routing":{"rules":` + rules + `}}`
	out, rep := mustBuild(t, baseInput(profile.Profile{Config: parseObj(t, []byte(cfg))}))
	m := decode(t, out)
	// First socks/http/mixed inbound is b.
	if dig(m, "inbounds", 0, "tag") != "b" {
		t.Fatalf("mixed tag = %v", dig(m, "inbounds", 0, "tag"))
	}
	root := parseObj(t, out)
	rt, _ := root.Get("routing")
	got, _ := rt.(*ojson.Object).Get("rules")
	g := got.([]any)
	want, _ := ojson.Parse([]byte(rules))
	gb, _ := ojson.Marshal(g[len(g)-2:])
	wb, _ := ojson.Marshal(want)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("provider rules changed:\n%s\n%s", gb, wb)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "[a c]") || strings.Contains(rep.Warnings[0], "other") {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	if marshalAny(t, rep.ReplacedInbounds) != `["a","b","c","http:4"]` {
		t.Fatalf("replaced = %v", rep.ReplacedInbounds)
	}

	// No socks/http/mixed inbound: the first inbound's tag.
	cfg = `{"inbounds":[{"tag":"x","protocol":"vmess","port":1},{"tag":"y","protocol":"vless","port":2}],
	"outbounds":[{"tag":"proxy","protocol":"vless"}],"routing":{"rules":[]}}`
	out, _ = mustBuild(t, baseInput(profile.Profile{Config: parseObj(t, []byte(cfg))}))
	if dig(decode(t, out), "inbounds", 0, "tag") != "x" {
		t.Fatal("fallback to first inbound tag")
	}
}

func TestProxyTagNeverDirect(t *testing.T) {
	cfg := `{"outbounds":[{"tag":"d","protocol":"freedom"},{"tag":"real","protocol":"vless"}],
	"routing":{"rules":[{"type":"field","network":"tcp,udp","outboundTag":"d"}]}}`
	mk := func(src profile.Source) profile.Profile {
		return profile.Profile{Config: parseObj(t, []byte(cfg)), RoutingSource: src}
	}
	out, rep := mustBuild(t, baseInput(mk(profile.Source{Kind: profile.SourceProvider})))
	if rep.ProxyTag != "real" || dig(ruleList(t, out)[1], "outboundTag") != "real" {
		t.Fatalf("provider: proxy tag %q rule %v", rep.ProxyTag, ruleList(t, out)[1])
	}
	out, rep = mustBuild(t, baseInput(mk(profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy})))
	rules := ruleList(t, out)
	if rep.ProxyTag != "real" || dig(rules[len(rules)-1], "outboundTag") != "real" {
		t.Fatalf("preset: proxy tag %q last rule %v", rep.ProxyTag, rules[len(rules)-1])
	}
	// Only internal outbounds: an error.
	only := profile.Profile{Config: parseObj(t, []byte(`{"outbounds":[{"tag":"d","protocol":"freedom"}]}`))}
	if _, _, err := Build(baseInput(only)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectBlockAddedOnlyWhenMissing(t *testing.T) {
	cfg := `{"outbounds":[{"tag":"direct","protocol":"vless"},{"tag":"zatvor-direct-1","protocol":"vless"}],"routing":{"rules":[]}}`
	out, _ := mustBuild(t, baseInput(profile.Profile{Config: parseObj(t, []byte(cfg))}))
	outs := decode(t, out)["outbounds"].([]any)
	if len(outs) != 4 || dig(outs, 2, "tag") != "zatvor-direct-2" || dig(outs, 3, "tag") != "block" {
		t.Fatalf("outbounds = %s", marshalAny(t, outs))
	}
	// The block rule uses the tag we added.
	if dig(ruleList(t, out)[0], "outboundTag") != "api" {
		t.Fatal("unexpected first rule")
	}

	// Fixture already has both: nothing added.
	p := configProfile(t, "xray-hy2-dns-finalmask.json", profile.Source{})
	out, _ = mustBuild(t, baseInput(p))
	if n := len(decode(t, out)["outbounds"].([]any)); n != 3 {
		t.Fatalf("outbounds = %d", n)
	}
}

func TestPresetOnConfigReplacesRoutingAndDNS(t *testing.T) {
	src := profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllExceptRU}
	out, rep := mustBuild(t, baseInput(configProfile(t, "xray-hy2-dns-finalmask.json", src)))
	m := decode(t, out)
	if _, ok := m["dns"]; ok {
		t.Fatal("provider dns kept under a preset")
	}
	if rep.ProxyTag != "proxy" || len(ruleList(t, out)) != 5 {
		t.Fatalf("rules = %d proxy = %q", len(ruleList(t, out)), rep.ProxyTag)
	}
}

func TestBuildDoesNotMutateInput(t *testing.T) {
	p := configProfile(t, "xray-hy2-dns-finalmask.json", profile.Source{})
	before, _ := ojson.Marshal(p.Config)
	mustBuild(t, baseInput(p))
	after, _ := ojson.Marshal(p.Config)
	if !bytes.Equal(before, after) {
		t.Fatal("Build mutated the provider config")
	}
}

func TestBuildErrors(t *testing.T) {
	good := baseInput(nodeProfile(t, realityLink, profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}))
	mod := func(f func(*Input)) Input { in := good; f(&in); return in }
	cases := map[string]Input{
		"zero port":       mod(func(i *Input) { i.Ports.API = 0 }),
		"duplicate port":  mod(func(i *Input) { i.Ports.Probe = i.Ports.Mixed }),
		"no probe pass":   mod(func(i *Input) { i.ProbePass = "" }),
		"bad log level":   mod(func(i *Input) { i.LogLevel = "loud" }),
		"unknown preset":  mod(func(i *Input) { i.Profile.RoutingSource.Preset = "x" }),
		"happ without it": mod(func(i *Input) { i.Profile.RoutingSource = profile.Source{Kind: profile.SourceHapp} }),
		"no node/config":  mod(func(i *Input) { i.Profile.Node = nil }),
	}
	for name, in := range cases {
		if _, _, err := Build(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A duplicate dev-tools port is fine while the feature is off.
	in := good
	in.Ports.DevTools = in.Ports.Mixed
	if _, _, err := Build(in); err != nil {
		t.Errorf("dev-tools port checked while off: %v", err)
	}
	bad := baseInput(profile.Profile{Config: parseObj(t, []byte(`{"outbounds":[]}`))})
	if _, _, err := Build(bad); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("empty outbounds: %v", err)
	}
}

func TestLogLevel(t *testing.T) {
	in := baseInput(nodeProfile(t, realityLink, profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}))
	out, _ := mustBuild(t, in)
	if dig(decode(t, out), "log", "loglevel") != "warning" {
		t.Fatal("default log level")
	}
	in.LogLevel = "debug"
	out, _ = mustBuild(t, in)
	if dig(decode(t, out), "log", "loglevel") != "debug" {
		t.Fatal("log level")
	}
}
