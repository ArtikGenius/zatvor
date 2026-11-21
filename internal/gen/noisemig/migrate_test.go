package noisemig

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const fixturePacket = "a3c185800001000100000000076578616d706c6503636f6d0000010001c00c000100010000105a00045db8d70e"

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixture(t *testing.T, name string) []byte {
	return readFile(t, "../../../testdata/fixtures/"+name)
}

func semanticEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func TestMigrateLegacyFixtureMatchesGolden(t *testing.T) {
	out, rep, err := Migrate(fixture(t, "xray-hy2-dns-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The research file's "remarks" was a hand-edited label; the golden keeps
	// the input's remark because Migrate never edits labels.
	golden := readFile(t, "../../../testdata/golden/noisemig/legacy-to-finalmask.json")
	if !semanticEqual(t, out, golden) {
		t.Fatalf("output differs from golden:\n%s", out)
	}
	if !reflect.DeepEqual(rep.Migrated, []string{"proxy"}) || len(rep.Refused) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestMigrateKeepsKeyOrder(t *testing.T) {
	out, _, err := Migrate(fixture(t, "xray-hy2-dns-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	order := []string{`"dns"`, `"inbounds"`, `"log"`, `"outbounds"`, `"protocol": "hysteria"`, `"tag": "proxy"`, `"streamSettings"`, `"finalmask"`, `"salamander"`, `"type": "noise"`, `"reset": 0`, `"hysteriaSettings"`, `"tlsSettings"`, `"policy"`, `"remarks"`, `"routing"`, `"stats"`}
	pos := -1
	for _, k := range order {
		i := strings.Index(s, k)
		if i <= pos {
			t.Fatalf("%s out of order (at %d, previous %d)", k, i, pos)
		}
		pos = i
	}
	for _, gone := range []string{`"dialerProxy"`, `"sockopt"`, `"dns-noise"`, `"noises"`} {
		if strings.Contains(s, gone) {
			t.Errorf("output still contains %q", gone)
		}
	}
}

func TestMigrateKeepsPacketBytes(t *testing.T) {
	out, _, err := Migrate(fixture(t, "xray-hy2-dns-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"packet": "`+fixturePacket+`"`)) {
		t.Fatalf("packet not preserved:\n%s", out)
	}
}

func TestMigrateFinalmaskFixtureIsNoop(t *testing.T) {
	in := fixture(t, "xray-hy2-dns-finalmask.json")
	out, rep, err := Migrate(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("output not byte-identical")
	}
	if len(rep.Migrated) != 0 || len(rep.Refused) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

// legacyConfig builds a minimal legacy config. proxyFinalmask is the proxy's
// finalmask object; freedom is the freedom outbound (plus any extra outbounds).
func legacyConfig(proxyFinalmask, freedom string) []byte {
	return []byte(`{
  "outbounds": [
    {
      "tag": "proxy",
      "protocol": "hysteria",
      "streamSettings": {
        "finalmask": ` + proxyFinalmask + `,
        "sockopt": {"dialerProxy": "dns-noise"}
      }
    },
    ` + freedom + `
  ]
}
`)
}

const (
	plainFinalmask = `{"udp": [{"type": "salamander", "settings": {"password": "x"}}]}`
	hexNoise       = `{"type": "hex", "packet": "` + fixturePacket + `", "delay": "5-10"}`
)

func TestMigrateRefuses(t *testing.T) {
	cases := []struct {
		name   string
		in     []byte
		reason string
	}{
		{"noise already in finalmask", legacyConfig(
			`{"udp": [{"type": "Noise", "settings": {"noise": []}}]}`,
			`{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [`+hexNoise+`]}}`),
			ReasonNoiseInFinalmask},
		{"chained dialer", legacyConfig(plainFinalmask,
			`{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [`+hexNoise+`]}, "streamSettings": {"sockopt": {"dialerProxy": "other"}}}, {"tag": "other", "protocol": "freedom"}`),
			ReasonChainedDialer},
		{"applyTo", legacyConfig(plainFinalmask,
			`{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [{"type": "hex", "packet": "00", "applyTo": "ipv4"}]}}`),
			ReasonApplyTo},
		{"redirect 53", legacyConfig(plainFinalmask,
			`{"tag": "dns-noise", "protocol": "freedom", "settings": {"redirect": "1.1.1.1:53", "noises": [`+hexNoise+`]}}`),
			ReasonRedirect53},
		{"freedom other settings", legacyConfig(plainFinalmask,
			`{"tag": "dns-noise", "protocol": "freedom", "settings": {"domainStrategy": "UseIPv4", "noises": [`+hexNoise+`]}}`),
			ReasonFreedomHasOtherSettings},
		{"freedom sockopt", legacyConfig(plainFinalmask,
			`{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [`+hexNoise+`]}, "streamSettings": {"sockopt": {"mark": 255}}}`),
			ReasonFreedomHasOtherSettings},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !json.Valid(c.in) {
				t.Fatalf("bad test input:\n%s", c.in)
			}
			out, rep, err := Migrate(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, c.in) {
				t.Fatalf("output not byte-identical:\n%s", out)
			}
			want := []Refusal{{Tag: "proxy", Reason: c.reason}}
			if !reflect.DeepEqual(rep.Refused, want) || len(rep.Migrated) != 0 {
				t.Fatalf("report = %+v, want refused %+v", rep, want)
			}
		})
	}
}

func TestMigrateIdempotent(t *testing.T) {
	once, _, err := Migrate(fixture(t, "xray-hy2-dns-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	twice, rep, err := Migrate(once)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(once, twice) {
		t.Fatalf("second pass changed output:\n%s", twice)
	}
	if len(rep.Migrated) != 0 || len(rep.Refused) != 0 {
		t.Fatalf("second pass report = %+v", rep)
	}
}

func TestMigrateItemTranslation(t *testing.T) {
	in := legacyConfig(plainFinalmask, `{"tag": "dns-noise", "protocol": "freedom", "settings": {"redirect": "", "noises": [
      {"type": "hex", "packet": " de ad\nbe ef ", "delay": 5},
      {"type": "str", "packet": "hello"},
      {"type": "base64", "packet": "-_8", "delay": "1-2"},
      {"type": "rand", "packet": "10-20", "delay": "3"}
    ]}}`)
	out, rep, err := Migrate(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Migrated, []string{"proxy"}) {
		t.Fatalf("report = %+v", rep)
	}
	want := `{"outbounds": [{"tag": "proxy", "protocol": "hysteria", "streamSettings": {"finalmask": {"udp": [
      {"type": "salamander", "settings": {"password": "x"}},
      {"type": "noise", "settings": {"reset": 0, "noise": [
        {"type": "hex", "packet": "deadbeef", "delay": 5},
        {"type": "str", "packet": "hello"},
        {"type": "base64", "packet": "+/8=", "delay": "1-2"},
        {"rand": "10-20", "delay": "3"}
      ]}}]}}}]}`
	if !semanticEqual(t, out, []byte(want)) {
		t.Fatalf("got:\n%s", out)
	}
}

func TestMigrateSharedFreedomAndReferences(t *testing.T) {
	in := []byte(`{
  "outbounds": [
    {"tag": "a", "protocol": "hysteria", "streamSettings": {"sockopt": {"dialerProxy": "dns-noise", "mark": 1}}},
    {"tag": "b", "protocol": "hysteria", "streamSettings": {"sockopt": {"dialerProxy": "dns-noise"}}},
    {"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [{"type": "str", "packet": "x"}]}}
  ],
  "routing": {"rules": [{"outboundTag": "dns-noise", "type": "field", "network": "udp"}]}
}`)
	out, rep, err := Migrate(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Migrated, []string{"a", "b"}) {
		t.Fatalf("report = %+v", rep)
	}
	want := `{
  "outbounds": [
    {"tag": "a", "protocol": "hysteria", "streamSettings": {"sockopt": {"mark": 1}, "finalmask": {"udp": [{"type": "noise", "settings": {"reset": 0, "noise": [{"type": "str", "packet": "x"}]}}]}}},
    {"tag": "b", "protocol": "hysteria", "streamSettings": {"finalmask": {"udp": [{"type": "noise", "settings": {"reset": 0, "noise": [{"type": "str", "packet": "x"}]}}]}}},
    {"tag": "dns-noise", "protocol": "freedom", "settings": {}}
  ],
  "routing": {"rules": [{"outboundTag": "dns-noise", "type": "field", "network": "udp"}]}
}`
	if !semanticEqual(t, out, []byte(want)) {
		t.Fatalf("got:\n%s", out)
	}
}

func TestMigrateBalancerPrefixKeepsFreedom(t *testing.T) {
	in := []byte(`{
  "outbounds": [
    {"tag": "p", "protocol": "hysteria", "streamSettings": {"sockopt": {"dialerProxy": "dns-noise"}}},
    {"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [{"type": "str", "packet": "x"}]}}
  ],
  "routing": {"balancers": [{"tag": "lb", "selector": ["dns"]}]}
}`)
	out, _, err := Migrate(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"tag": "dns-noise"`)) {
		t.Fatalf("freedom removed although a balancer selector matches it:\n%s", out)
	}
}

func TestMigrateErrors(t *testing.T) {
	bad := map[string][]byte{
		"not json":      []byte(`{`),
		"bad hex":       legacyConfig(plainFinalmask, `{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [{"type": "hex", "packet": "zz"}]}}`),
		"bad base64":    legacyConfig(plainFinalmask, `{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [{"type": "base64", "packet": "!!"}]}}`),
		"unknown type":  legacyConfig(plainFinalmask, `{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [{"type": "HEX", "packet": "00"}]}}`),
		"unknown field": legacyConfig(plainFinalmask, `{"tag": "dns-noise", "protocol": "freedom", "settings": {"noises": [{"type": "str", "packet": "x", "extra": 1}]}}`),
	}
	for name, in := range bad {
		out, _, err := Migrate(in)
		if err == nil {
			t.Errorf("%s: want error", name)
		}
		if !bytes.Equal(out, in) {
			t.Errorf("%s: output must be the input on error", name)
		}
	}
}

func TestMigrateDropsEmptyNoisesOnly(t *testing.T) {
	in := []byte(`{"outbounds": [{"tag": "direct", "protocol": "freedom", "settings": {"noises": [], "redirect": ""}}]}`)
	out, rep, err := Migrate(in)
	if err != nil {
		t.Fatal(err)
	}
	if !semanticEqual(t, out, []byte(`{"outbounds": [{"tag": "direct", "protocol": "freedom", "settings": {"redirect": ""}}]}`)) {
		t.Fatalf("got %s", out)
	}
	if len(rep.Migrated) != 0 || len(rep.Refused) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestMigrateLegacyFixtureGoldenBytes(t *testing.T) {
	out, _, err := Migrate(fixture(t, "xray-hy2-dns-legacy.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden := readFile(t, "../../../testdata/golden/noisemig/legacy-to-finalmask.json")
	if !bytes.Equal(out, golden) {
		t.Fatalf("output is not byte-identical to the golden (layout or key order differs):\n%s", out)
	}
}
