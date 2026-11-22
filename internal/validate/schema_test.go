package validate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func corpus(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, pat := range []string{"fixtures/xray-hy2-dns-*.json", "golden/gen/*.json"} {
		m, err := filepath.Glob(filepath.Join("..", "..", "testdata", pat))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) < 7 {
		t.Fatalf("expected at least 7 corpus files, got %v", files)
	}
	return files
}

func TestSchemaFixturesClean(t *testing.T) {
	for _, f := range corpus(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		w, err := Schema(b)
		if err != nil || len(w) != 0 {
			t.Errorf("%s: warnings=%v err=%v", filepath.Base(f), w, err)
		}
	}
}

// TestMapCoversCorpus: every protocol/network/security the fixtures and goldens
// use is known to the explicit dispatch maps.
func TestMapCoversCorpus(t *testing.T) {
	for _, f := range corpus(t) {
		b, _ := os.ReadFile(f)
		var c struct {
			Inbounds  []json.RawMessage `json:"inbounds"`
			Outbounds []json.RawMessage `json:"outbounds"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		for i, side := range [][]json.RawMessage{c.Inbounds, c.Outbounds} {
			for _, raw := range side {
				var o struct {
					Protocol       string
					StreamSettings struct{ Network, Security string }
				}
				if err := json.Unmarshal(raw, &o); err != nil {
					t.Fatal(err)
				}
				m := outboundSettings
				if i == 0 {
					m = inboundSettings
				}
				if _, ok := m[strings.ToLower(o.Protocol)]; !ok {
					t.Errorf("%s: protocol %q missing from map", filepath.Base(f), o.Protocol)
				}
				if n := o.StreamSettings.Network; n != "" && !knownNetworks[strings.ToLower(n)] {
					t.Errorf("%s: network %q missing from map", filepath.Base(f), n)
				}
				if s := o.StreamSettings.Security; s != "" && !knownSecurity[strings.ToLower(s)] {
					t.Errorf("%s: security %q missing from map", filepath.Base(f), s)
				}
			}
		}
	}
}

func TestUnknownKey(t *testing.T) {
	cfg := `{"outbounds":[{"protocol":"vless","tag":"p","settings":{"adress":"example.com","vnext":[]}}]}`
	w, err := Schema([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 || w[0] != "outbounds[0].settings.adress: unknown key" {
		t.Fatalf("warnings = %v", w)
	}
}

func TestUnknownKeyCaseInsensitive(t *testing.T) {
	w, err := Schema([]byte(`{"Outbounds":[{"Protocol":"freedom","TAG":"d","settings":{}}],"remarks":"x"}`))
	if err != nil || len(w) != 0 {
		t.Fatalf("warnings=%v err=%v", w, err)
	}
}

func TestUnknownRootAndNested(t *testing.T) {
	w, err := Schema([]byte(`{"bogus":1,"log":{"loglevel":"warning","nope":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bogus: unknown key", "log.nope: unknown key"}
	if strings.Join(w, "|") != strings.Join(want, "|") {
		t.Fatalf("warnings = %v", w)
	}
}

func TestWrongType(t *testing.T) {
	cases := map[string]string{
		"port string":      `{"outbounds":[{"protocol":"vless","settings":{"address":"example.com","port":"x","id":"u"}}]}`,
		"log not object":   `{"log":"debug"}`,
		"outbounds obj":    `{"outbounds":{}}`,
		"bool as string":   `{"outbounds":[{"protocol":"freedom","tag":true}]}`,
		"bad inbound port": `{"inbounds":[{"protocol":"socks","port":"x"}]}`,
		"not json":         `{`,
	}
	for name, cfg := range cases {
		if _, err := Schema([]byte(cfg)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := Schema([]byte(cases["port string"]))
	if err == nil || !strings.Contains(err.Error(), "outbounds[0].settings.port") {
		t.Fatalf("err = %v", err)
	}
	for cfg, path := range map[string]string{
		cases["port string"]:    "outbounds[0].settings.port",
		cases["log not object"]: "log",
		cases["not json"]:       "",
		`{} {}`:                 "",
	} {
		_, err := Schema([]byte(cfg))
		var se *SchemaError
		if !errors.As(err, &se) || se.Path != path {
			t.Errorf("%s: err = %#v, want SchemaError{Path: %q}", cfg, err, path)
		}
	}
}

func TestUnmarshalerShapesAccepted(t *testing.T) {
	cfg := `{"inbounds":[
	  {"protocol":"socks","port":"1080-1090","listen":"127.0.0.1","settings":{"auth":"noauth","udp":true}},
	  {"protocol":"http","port":8080,"settings":{}}],
	 "outbounds":[{"protocol":"freedom","settings":{"domainStrategy":"UseIP"}}]}`
	w, err := Schema([]byte(cfg))
	if err != nil || len(w) != 0 {
		t.Fatalf("warnings=%v err=%v", w, err)
	}
}

func TestProtocolNotChecked(t *testing.T) {
	w, err := Schema([]byte(`{"outbounds":[{"protocol":"futureproto","settings":{"anything":1}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 || w[0] != `outbounds[0]: protocol "futureproto" not checked` {
		t.Fatalf("warnings = %v", w)
	}
}

func TestStreamSettingsChecked(t *testing.T) {
	cfg := `{"outbounds":[{"protocol":"freedom","streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverNme":"x"}}}]}`
	w, err := Schema([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 || w[0] != "outbounds[0].streamSettings.tlsSettings.serverNme: unknown key" {
		t.Fatalf("warnings = %v", w)
	}
}
