package sysapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRecorderWritesAndChangesNothing(t *testing.T) {
	var buf bytes.Buffer
	r := NewRecorder(&buf)
	want := ProxyState{Enabled: true, Server: "127.0.0.1:10808", Bypass: []string{"localhost", "<local>"}}
	if err := r.Set(want); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled != want.Enabled || got.Server != want.Server || strings.Join(got.Bypass, ",") != strings.Join(want.Bypass, ",") {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("log has %d lines, want 1 (reads are not recorded):\n%s", len(lines), buf.String())
	}
	for i, op := range []string{"proxy.set"} {
		var e struct {
			TS   string          `json:"ts"`
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &e); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if e.Op != op {
			t.Fatalf("line %d op = %q, want %q", i, e.Op, op)
		}
		if _, err := time.Parse(time.RFC3339Nano, e.TS); err != nil {
			t.Fatalf("line %d ts %q: %v", i, e.TS, err)
		}
		if e.Args == nil {
			t.Fatalf("line %d has no args", i)
		}
	}
	// Mutating the caller's slice afterwards must not change what was recorded.
	want.Bypass[0] = "changed"
	if got, _ := r.Get(); got.Bypass[0] != "localhost" {
		t.Fatalf("recorder aliases caller slice: %v", got.Bypass)
	}
}

func TestRecorderGetZeroAndProcessOps(t *testing.T) {
	var buf bytes.Buffer
	r := NewRecorder(&buf)
	if got, err := r.Get(); err != nil || got.Enabled || got.Server != "" || len(got.Bypass) != 0 {
		t.Fatalf("fresh Get = %+v, %v; want zero", got, err)
	}
	id := ProcID{PID: 4242, Created: time.Unix(1700000000, 5).UTC()}
	var pc ProcessControl = r
	for _, f := range []func(ProcID) error{pc.Suspend, pc.Resume, pc.Terminate} {
		if err := f(id); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	wantOps := []string{"proc.suspend", "proc.resume", "proc.terminate"}
	if len(lines) != len(wantOps) {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	for i, op := range wantOps {
		if !strings.Contains(lines[i], `"op":"`+op+`"`) {
			t.Fatalf("line %d = %s, want op %s", i, lines[i], op)
		}
	}
	if !strings.Contains(lines[0], `"pid":4242`) {
		t.Fatalf("suspend args missing pid: %s", lines[0])
	}
}

var _ SystemProxy = (*Recorder)(nil)

func TestRecorderPerConnection(t *testing.T) {
	var buf bytes.Buffer
	r := NewRecorder(&buf)
	names, err := r.Connections()
	if err != nil || len(names) != 0 {
		t.Fatalf("Connections = %v, %v; want none", names, err)
	}
	lan := ProxyState{Enabled: true, Server: "127.0.0.1:10808"}
	ras := ProxyState{PACURL: "http://pac.example.test/p.pac", AutoDetect: true}
	if err := r.SetConn("", lan); err != nil {
		t.Fatal(err)
	}
	if err := r.SetConn("Office VPN", ras); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Get(); err != nil || got.Server != lan.Server || !got.Enabled {
		t.Fatalf("Get after SetConn(\"\") = %+v, %v; want %+v", got, err, lan)
	}
	if got, err := r.GetConn("Office VPN"); err != nil || got.PACURL != ras.PACURL || !got.AutoDetect || got.Enabled {
		t.Fatalf("GetConn(Office VPN) = %+v, %v; want %+v", got, err, ras)
	}
	if got, err := r.GetConn("Other"); err != nil || got.Enabled || got.PACURL != "" {
		t.Fatalf("GetConn(unset) = %+v, %v; want zero", got, err)
	}
	wantOps := []string{"proxy.set", "proxy.set_conn"}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != len(wantOps) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(wantOps), buf.String())
	}
	for i, op := range wantOps {
		if !strings.Contains(lines[i], `"op":"`+op+`"`) {
			t.Fatalf("line %d = %s, want op %s", i, lines[i], op)
		}
	}
	if !strings.Contains(lines[1], `"conn":"Office VPN"`) || !strings.Contains(lines[1], `"auto_detect":true`) {
		t.Fatalf("set_conn args = %s, want conn and state", lines[1])
	}
}
