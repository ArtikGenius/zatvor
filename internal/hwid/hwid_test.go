package hwid

import (
	"regexp"
	"testing"
)

// goldenVector freezes the algorithm (ADR 0008). It must never change.
const goldenVector = "32fe07fe2e30f9601fdac3197ad16506"

func TestHWIDVector(t *testing.T) {
	got := ID("00000000-0000-0000-0000-000000000000", "sub.example.com")
	if got != goldenVector {
		t.Fatalf("HWID algorithm changed: got %s", got)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9=-]{10,64}$`).MatchString(got) || len(got) != 32 {
		t.Fatalf("bad shape %q", got)
	}
	if ID("00000000-0000-0000-0000-000000000000", "other.example.com") == got {
		t.Fatal("must differ per host")
	}
	if ID("00000000-0000-0000-0000-000000000001", "sub.example.com") == got {
		t.Fatal("must differ per machine")
	}
}

func TestHeaders(t *testing.T) {
	dev := Device{OS: "windows", OSVersion: "11", Model: "PC"}
	h := Headers("m", "sub.example.com", dev, Options{SendDevice: true})
	if h.Get("X-Hwid") != ID("m", "sub.example.com") || h.Get("X-Device-Os") != "windows" ||
		h.Get("X-Ver-Os") != "11" || h.Get("X-Device-Model") != "PC" {
		t.Fatalf("%v", h)
	}
	h = Headers("m", "sub.example.com", dev, Options{})
	if h.Get("X-Hwid") == "" || h.Get("X-Device-Os") != "" || h.Get("X-Ver-Os") != "" || h.Get("X-Device-Model") != "" {
		t.Fatalf("device headers must be off: %v", h)
	}
	h = Headers("m", "h", Device{OS: "linux"}, Options{SendDevice: true})
	if h.Get("X-Device-Os") != "linux" || h.Get("X-Ver-Os") != "" {
		t.Fatalf("empty fields omitted: %v", h)
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"Sub.Example.COM:8443": "sub.example.com",
		"sub.example.com.":     "sub.example.com",
		"[2001:DB8::1]:443":    "2001:db8::1",
		"[2001:DB8::1]":        "2001:db8::1",
		"fe80::1%eth0":         "fe80::1",
		"[fe80::1%eth0]:80":    "fe80::1",
		"пример.рф":            "xn--e1afmkfd.xn--p1ai",
		"  sub.example.com ":   "sub.example.com",
		"sub.example.com":      "sub.example.com",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
		if ID("m", in) != ID("m", want) {
			t.Errorf("ID differs for %q and %q", in, want)
		}
	}
}

func TestMachineIDNormalised(t *testing.T) {
	want := ID("abc-def", "sub.example.com")
	for _, in := range []string{"ABC-DEF", "  abc-def\n", " ABC-def "} {
		if ID(in, "sub.example.com") != want {
			t.Errorf("machine id %q not normalised", in)
		}
	}
	if got := ID("00000000-0000-0000-0000-000000000000", "Sub.Example.com:443"); got != goldenVector {
		t.Fatalf("golden vector changed: %s", got)
	}
}
