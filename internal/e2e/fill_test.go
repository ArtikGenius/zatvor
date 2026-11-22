package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFillReplacesWholeValues(t *testing.T) {
	tmpl := []byte(`{"port":"REPLACE_ME_PORT","a":["REPLACE_ME_X","keep"],"via":"REPLACE_ME_GONE","n":{"s":"REPLACE_ME_X suffix"}}`)
	_, err := Fill(tmpl, map[string]any{"REPLACE_ME_PORT": json.Number("1234"), "REPLACE_ME_X": []any{"l1", "l2"}, "REPLACE_ME_GONE": nil})
	if err == nil || !strings.Contains(err.Error(), "REPLACE_ME_X suffix") {
		t.Fatalf("partial placeholder: err = %v, want a leftover-placeholder error", err)
	}

	tmpl = []byte(`{"port":"REPLACE_ME_PORT","a":["REPLACE_ME_X","keep"],"via":"REPLACE_ME_GONE"}`)
	out, err := Fill(tmpl, map[string]any{"REPLACE_ME_PORT": json.Number("1234"), "REPLACE_ME_X": []any{"l1", "l2"}, "REPLACE_ME_GONE": nil})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["port"] != float64(1234) {
		t.Errorf("port = %v", got["port"])
	}
	if a, _ := got["a"].([]any); len(a) != 2 || a[1] != "keep" {
		t.Errorf("a = %v", got["a"])
	}
	if _, ok := got["via"]; ok {
		t.Error("nil value did not remove the member")
	}
}

func TestFillRejectsRemovalInList(t *testing.T) {
	if _, err := Fill([]byte(`["REPLACE_ME_GONE"]`), map[string]any{"REPLACE_ME_GONE": nil}); err == nil {
		t.Fatal("removing a list element: want an error")
	}
}

func TestFillMissingValue(t *testing.T) {
	if _, err := Fill([]byte(`{"id":"REPLACE_ME_UUID"}`), nil); err == nil {
		t.Fatal("unfilled placeholder: want an error")
	}
}
