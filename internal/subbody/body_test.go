package subbody

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "subs", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJSONObject(t *testing.T) {
	r := Decode(read(t, "object.json"))
	if r.Kind != XrayJSON || len(r.Configs) != 1 {
		t.Fatalf("kind=%v configs=%d", r.Kind, len(r.Configs))
	}
}

func TestJSONArray(t *testing.T) {
	r := Decode(read(t, "array.json"))
	if r.Kind != XrayJSON || len(r.Configs) != 2 {
		t.Fatalf("kind=%v configs=%d", r.Kind, len(r.Configs))
	}
}

func TestJSONWithoutOutbounds(t *testing.T) {
	if r := Decode(read(t, "no-outbounds.json")); r.Kind != Unknown {
		t.Fatalf("kind=%v", r.Kind)
	}
}

func TestBase64Variants(t *testing.T) {
	plain := read(t, "links3.txt")
	want := Decode(plain)
	if want.Kind != Links || len(want.Nodes) != 3 {
		t.Fatalf("plain: kind=%v nodes=%d", want.Kind, len(want.Nodes))
	}
	wrap := func(s string) string {
		var b strings.Builder
		for len(s) > 40 {
			b.WriteString(s[:40] + "\r\n")
			s = s[40:]
		}
		return b.String() + s
	}
	cases := map[string]string{
		"std":     base64.StdEncoding.EncodeToString(plain),
		"url":     base64.URLEncoding.EncodeToString(plain),
		"rawstd":  base64.RawStdEncoding.EncodeToString(plain),
		"rawurl":  base64.RawURLEncoding.EncodeToString(plain),
		"crlf":    wrap(base64.StdEncoding.EncodeToString(plain)),
		"bom":     "\xef\xbb\xbf" + base64.StdEncoding.EncodeToString(plain),
		"bomcrlf": "\xef\xbb\xbf" + wrap(base64.RawURLEncoding.EncodeToString(plain)) + "\r\n",
	}
	for name, body := range cases {
		r := Decode([]byte(body))
		if r.Kind != Links || len(r.Nodes) != 3 {
			t.Errorf("%s: kind=%v nodes=%d", name, r.Kind, len(r.Nodes))
			continue
		}
		for i := range r.Nodes {
			if r.Nodes[i].Raw != want.Nodes[i].Raw {
				t.Errorf("%s: node %d differs", name, i)
			}
		}
	}
}

func TestPlainLines(t *testing.T) {
	r := Decode(read(t, "plain-headers.txt"))
	if r.Kind != Links || len(r.Nodes) != 2 || len(r.Skipped) != 0 {
		t.Fatalf("kind=%v nodes=%d skipped=%v", r.Kind, len(r.Nodes), r.Skipped)
	}
	if got := r.InlineHeaders["profile-title"]; got != "base64:UkVQTEFDRV9NRQ==" {
		t.Errorf("profile-title=%q", got)
	}
	if got := r.InlineHeaders["subscription-userinfo"]; got != "upload=0; download=0; total=0; expire=0" {
		t.Errorf("userinfo=%q", got)
	}
	if len(r.InlineHeaders) != 2 {
		t.Errorf("headers=%v", r.InlineHeaders)
	}
}

func TestOneBadLine(t *testing.T) {
	r := Decode(read(t, "one-bad.txt"))
	if r.Kind != Links || len(r.Nodes) != 2 {
		t.Fatalf("kind=%v nodes=%d", r.Kind, len(r.Nodes))
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Line != 2 || r.Skipped[0].Scheme != "vless" {
		t.Fatalf("skipped=%+v", r.Skipped)
	}
	if strings.Contains(r.Skipped[0].Reason, "broken") {
		t.Error("reason quotes the line")
	}
}

func TestUnknownSchemeSkipped(t *testing.T) {
	body := string(read(t, "links3.txt")) + "foo://bar\n"
	r := Decode([]byte(body))
	if r.Kind != Links || len(r.Nodes) != 3 || len(r.Skipped) != 1 || r.Skipped[0].Scheme != "foo" {
		t.Fatalf("kind=%v nodes=%d skipped=%+v", r.Kind, len(r.Nodes), r.Skipped)
	}
}

func TestClash(t *testing.T) {
	if r := Decode(read(t, "clash.yaml")); r.Kind != Clash {
		t.Fatalf("kind=%v", r.Kind)
	}
}

func TestEncryptedBody(t *testing.T) {
	if r := Decode(read(t, "encrypted.txt")); r.Kind != Encrypted {
		t.Fatalf("kind=%v", r.Kind)
	}
}

func TestEncryptedMixedWithGood(t *testing.T) {
	body := string(read(t, "encrypted.txt")) + string(read(t, "links3.txt"))
	r := Decode([]byte(body))
	if r.Kind != Links || len(r.Nodes) != 3 || len(r.Skipped) != 1 || r.Skipped[0].Reason != "encrypted" {
		t.Fatalf("kind=%v nodes=%d skipped=%+v", r.Kind, len(r.Nodes), r.Skipped)
	}
}

func TestEmpty(t *testing.T) {
	for _, b := range []string{"", " \r\n\t\n ", "\xef\xbb\xbf  \n"} {
		if r := Decode([]byte(b)); r.Kind != Empty {
			t.Errorf("%q: kind=%v", b, r.Kind)
		}
	}
}

func TestHTML(t *testing.T) {
	if r := Decode(read(t, "page.html")); r.Kind != Unknown {
		t.Fatalf("kind=%v", r.Kind)
	}
}

func TestTenThousandNodes(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&b, "vless://REPLACE_ME_uuid@h%d.example.com:443?encryption=none&security=tls&sni=h%d.example.com#n%d\n", i, i, i)
	}
	for name, body := range map[string]string{
		"plain":  b.String(),
		"base64": base64.StdEncoding.EncodeToString([]byte(b.String())),
	} {
		start := time.Now()
		r := Decode([]byte(body))
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: took %v", name, d)
		}
		if r.Kind != Links || len(r.Nodes) != 10000 {
			t.Errorf("%s: kind=%v nodes=%d", name, r.Kind, len(r.Nodes))
		}
	}
}
