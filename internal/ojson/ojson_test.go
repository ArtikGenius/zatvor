package ojson

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrderedRoundTripKeepsKeyOrder(t *testing.T) {
	in := "{\n  \"z\": 1,\n  \"a\": {\n    \"y\": [\n      true,\n      null,\n      \"<r 2> ·\"\n    ],\n    \"b\": 12345678901234567890\n  },\n  \"e\": {},\n  \"f\": []\n}\n"
	v, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip changed text:\n%s\nwant:\n%s", out, in)
	}
}

func TestOrderedObjectOps(t *testing.T) {
	v, err := Parse([]byte(`{"a":1,"b":2,"c":3}`))
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*Object)
	o.Delete("b")
	o.Set("d", "x")
	o.Set("a", "y")
	out, _ := Marshal(o)
	want := "{\n  \"a\": \"y\",\n  \"c\": 3,\n  \"d\": \"x\"\n}\n"
	if string(out) != want {
		t.Fatalf("got %s want %s", out, want)
	}
	if _, ok := o.Get("b"); ok {
		t.Fatal("b still present")
	}
}

func TestOrderedRejectsTrailingData(t *testing.T) {
	for _, in := range []string{`{} {}`, `{"a":}`, `[1,`, ``} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%q: want error", in)
		} else if strings.Contains(err.Error(), "panic") {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestOrderedKeys(t *testing.T) {
	v, err := Parse([]byte(`{"z":1,"a":2,"z":3}`))
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*Object)
	if got := strings.Join(o.Keys(), ","); got != "z,a" {
		t.Fatalf("Keys = %s, want z,a", got)
	}
	if o.Len() != 3 {
		t.Fatalf("Len = %d, want 3", o.Len())
	}
	if k, _ := o.At(1); k != "a" {
		t.Fatalf("At(1) key = %s", k)
	}
}

func TestObjectJSONRoundTrip(t *testing.T) {
	type holder struct {
		Cfg  *Object `json:"cfg"`
		Null *Object `json:"null"`
	}
	in := `{"cfg":{"z":1,"a":{"y":[true,null,"x"],"b":12345678901234567890},"m":1.50},"null":null}`
	var h holder
	if err := json.Unmarshal([]byte(in), &h); err != nil {
		t.Fatal(err)
	}
	if h.Null != nil {
		t.Fatal("null member should stay nil")
	}
	if got := strings.Join(h.Cfg.Keys(), ","); got != "z,a,m" {
		t.Fatalf("key order = %s", got)
	}
	if v, _ := h.Cfg.Get("z"); v != json.Number("1") {
		t.Fatalf("z = %#v, want json.Number", v)
	}
	out, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip:\n%s\nwant:\n%s", out, in)
	}
	if err := json.Unmarshal([]byte(`{"cfg":[1]}`), &h); err == nil {
		t.Fatal("array into Object must fail")
	}
}
