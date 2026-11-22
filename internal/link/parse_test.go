package link

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEncrypted(t *testing.T) {
	for _, l := range []string{
		"happ://crypt3/REPLACE_ME_blob",
		"happ://crypt/REPLACE_ME_blob",
		"happ://crypt5/REPLACE_ME_blob",
		"incy://REPLACE_ME_blob",
	} {
		_, err := Parse(l)
		if !errors.Is(err, ErrEncrypted) {
			t.Errorf("%.12s: err = %v, want ErrEncrypted", l, err)
		}
		if err != nil && strings.Contains(err.Error(), "REPLACE_ME") {
			t.Errorf("error leaks the line: %v", err)
		}
	}
}

func TestUnknownScheme(t *testing.T) {
	_, err := Parse("tuic://REPLACE_ME_secret@tuic.example.com:443#x")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if strings.Contains(err.Error(), "REPLACE_ME") || strings.Contains(err.Error(), "example.com") {
		t.Fatalf("error leaks the line: %v", err)
	}
	if !strings.Contains(err.Error(), "tuic") {
		t.Fatalf("error should name the scheme: %v", err)
	}
	if _, err := Parse("not a link"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("no scheme: err = %v", err)
	}
}

func TestBadInput(t *testing.T) {
	for _, l := range []string{
		"",
		"vless://REPLACE_ME@host.example.com:0?security=none",
		"vless://REPLACE_ME@host.example.com:65536?security=none",
		"vless://REPLACE_ME@host.example.com?security=none",
		"vless://@host.example.com:443",
		"vless://REPLACE_ME@[2001:db8::1:443",
		"vmess://!!!notbase64!!!",
		"vmess://e30", // {} : no address
		"ss://REPLACE_ME",
		"hysteria2://host.example.com:443",
	} {
		_, err := Parse(l)
		if err == nil {
			t.Errorf("%q: want error", l)
			continue
		}
		if strings.Contains(err.Error(), "REPLACE_ME") || strings.Contains(err.Error(), "example.com") {
			t.Errorf("%q: error leaks the line: %v", l, err)
		}
	}
}

func TestRemarkSerializer(t *testing.T) {
	remark := `a"b\c#d%e ✓ Ж`
	nodes := []Node{
		{Scheme: "vless", Params: map[string]string{"id": "REPLACE_ME"}},
		{Scheme: "trojan", Params: map[string]string{"password": "REPLACE_ME"}},
		{Scheme: "ss", Params: map[string]string{"method": "aes-256-gcm", "password": "REPLACE_ME"}},
		{Scheme: "hysteria2", Params: map[string]string{"password": "REPLACE_ME"}},
		{Scheme: "socks", Params: map[string]string{"user": "REPLACE_ME", "password": "REPLACE_ME"}},
		{Scheme: "vmess", Params: map[string]string{"id": "REPLACE_ME", "net": "tcp"}},
	}
	for _, n := range nodes {
		n.Remark, n.Address, n.Port = remark, "host.example.com", 443
		got, err := Parse(Export(n))
		if err != nil {
			t.Fatalf("%s: %v", n.Scheme, err)
		}
		got.Raw = ""
		if !reflect.DeepEqual(got, n) {
			t.Errorf("%s: node changed:\n got %+v\nwant %+v", n.Scheme, got, n)
		}
	}
}

func TestID(t *testing.T) {
	a, _ := Parse("vless://REPLACE_ME@host.example.com:443?security=tls&type=ws&path=%2Fp&host=h.example.com#one")
	b, _ := Parse("vless://REPLACE_ME@host.example.com:443?host=h.example.com&path=%2Fp&type=ws&security=tls&flow=x#two")
	if ID(a) != ID(b) || len(ID(a)) != 16 {
		t.Fatalf("ID should ignore remark, order and non-identity params: %q %q", ID(a), ID(b))
	}
	c, _ := Parse("vless://REPLACE_ME@host.example.com:443?security=tls&type=ws&path=%2Fq&host=h.example.com#one")
	if ID(a) == ID(c) {
		t.Fatal("ID should change with the path")
	}
	d, _ := Parse("vless://REPLACE_ME@host.example.org:443?security=tls&type=ws&path=%2Fp&host=h.example.com#one")
	if ID(a) == ID(d) {
		t.Fatal("ID should change with the address")
	}
}
