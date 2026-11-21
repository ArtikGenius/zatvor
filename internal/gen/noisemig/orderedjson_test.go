package noisemig

import (
	"strings"
	"testing"
)

func TestOrderedRoundTripKeepsKeyOrder(t *testing.T) {
	in := "{\n  \"z\": 1,\n  \"a\": {\n    \"y\": [\n      true,\n      null,\n      \"<r 2> ·\"\n    ],\n    \"b\": 12345678901234567890\n  },\n  \"e\": {},\n  \"f\": []\n}\n"
	v, err := parseOrdered([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := encodeOrdered(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip changed text:\n%s\nwant:\n%s", out, in)
	}
}

func TestOrderedObjectOps(t *testing.T) {
	v, err := parseOrdered([]byte(`{"a":1,"b":2,"c":3}`))
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*object)
	o.del("b")
	o.set("d", "x")
	o.set("a", "y")
	out, _ := encodeOrdered(o)
	want := "{\n  \"a\": \"y\",\n  \"c\": 3,\n  \"d\": \"x\"\n}\n"
	if string(out) != want {
		t.Fatalf("got %s want %s", out, want)
	}
	if _, ok := o.get("b"); ok {
		t.Fatal("b still present")
	}
}

func TestOrderedRejectsTrailingData(t *testing.T) {
	for _, in := range []string{`{} {}`, `{"a":}`, `[1,`, ``} {
		if _, err := parseOrdered([]byte(in)); err == nil {
			t.Errorf("%q: want error", in)
		} else if strings.Contains(err.Error(), "panic") {
			t.Errorf("%q: %v", in, err)
		}
	}
}
