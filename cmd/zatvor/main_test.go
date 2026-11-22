package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeAPI is a daemon API on 127.0.0.1 with a real discovery and token
// file in a temp data directory.
type fakeAPI struct {
	t       *testing.T
	dataDir string
	token   string
	srv     *httptest.Server
	hits    atomic.Int32
}

// newFakeAPI serves routes (by "METHOD /path") behind the daemon's token
// and Host check.
func newFakeAPI(t *testing.T, routes map[string]http.HandlerFunc) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, dataDir: newDataDir(t), token: randomToken(t)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if r.Host != f.srv.Listener.Addr().String() || r.Header.Get("Authorization") != "Bearer "+f.token {
			writeJSON(w, 403, `{"code":"forbidden","message":"forbidden"}`)
			return
		}
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			writeJSON(w, 404, `{"code":"not_found","message":"no such endpoint"}`)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	writeDiscovery(t, f.dataDir, f.srv.Listener.Addr().(*net.TCPAddr).Port, f.token)
	return f
}

func newDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	for _, sub := range []string{"run", "journal"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func writeDiscovery(t *testing.T, dataDir string, port int, token string) {
	t.Helper()
	disc := fmt.Sprintf(`{"version":1,"port":%d,"pid":1}`, port)
	if err := os.WriteFile(filepath.Join(dataDir, "run", "token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "run", "api.json"), []byte(disc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, body)
}

func serve(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, status, body) }
}

// zatvor runs the command and checks that its output never holds token.
func zatvor(t *testing.T, token string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, func(string) string { return "" }, &out, &errb)
	if token != "" && (strings.Contains(out.String(), token) || strings.Contains(errb.String(), token)) {
		t.Errorf("zatvor %q printed the token", args)
	}
	return code, out.String(), errb.String()
}

// refusedPort is a loopback port nothing listens on.
func refusedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestCheckExitCodes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   int
		result string
	}{
		{"ok", 200, `{"status":"ok","ipv4":"203.0.113.9","at":"2026-11-22T10:00:00Z","gen":1}`, 0, "ok"},
		{"bad", 200, `{"status":"bad","at":"2026-11-22T10:00:00Z","gen":1}`, 1, "bad"},
		{"unknown", 200, `{"status":"unknown","at":"2026-11-22T10:00:00Z","gen":1}`, 2, "unknown"},
		{"not connected", 409, `{"code":"not_connected","message":"not connected"}`, 1, "not_connected"},
		{"check canceled", 422, `{"code":"canceled","message":"tunnel check canceled"}`, 2, "error"},
		{"internal", 500, `{"code":"internal","message":"internal error"}`, 2, "error"},
		{"not JSON", 200, `<html>`, 2, "error"},
		// The port answers but refuses our token: another daemon or program.
		{"stale token", 403, `{"code":"forbidden","message":"forbidden"}`, 3, "daemon_not_running"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t, map[string]http.HandlerFunc{"POST /v1/check": serve(tc.status, tc.body)})
			code, out, errOut := zatvor(t, f.token, "check", "--data-dir", f.dataDir)
			if code != tc.want {
				t.Errorf("check = %d, want %d (stdout %q, stderr %q)", code, tc.want, out, errOut)
			}
			code, out, _ = zatvor(t, f.token, "check", "--json", "--data-dir", f.dataDir)
			var got struct {
				Result string `json:"result"`
				Exit   int    `json:"exit"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil || code != tc.want || got.Exit != tc.want || got.Result != tc.result {
				t.Errorf("check --json = %d %q (%v), want exit %d result %q", code, out, err, tc.want, tc.result)
			}
		})
	}

	t.Run("no discovery file", func(t *testing.T) {
		dir := newDataDir(t)
		if code, out, errOut := zatvor(t, "", "check", "--data-dir", dir); code != 3 || !strings.Contains(errOut, "not running") {
			t.Errorf("check = %d (%q, %q), want 3", code, out, errOut)
		}
		if code, out, _ := zatvor(t, "", "check", "--json", "--data-dir", dir); code != 3 || !strings.Contains(out, `"result": "daemon_not_running"`) {
			t.Errorf("check --json = %d %q, want 3", code, out)
		}
	})
	t.Run("port refuses connections", func(t *testing.T) {
		dir := newDataDir(t)
		writeDiscovery(t, dir, refusedPort(t), randomToken(t))
		if code, out, errOut := zatvor(t, "", "check", "--data-dir", dir); code != 3 {
			t.Errorf("check = %d (%q, %q), want 3", code, out, errOut)
		}
	})
	t.Run("damaged token file", func(t *testing.T) {
		f := newFakeAPI(t, map[string]http.HandlerFunc{"POST /v1/check": serve(200, `{"status":"ok"}`)})
		if err := os.WriteFile(filepath.Join(f.dataDir, "run", "token"), []byte("short"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := zatvor(t, f.token, "check", "--data-dir", f.dataDir); code != 3 || f.hits.Load() != 0 {
			t.Errorf("check = %d with %d requests, want 3 and none", code, f.hits.Load())
		}
	})
	t.Run("usage errors", func(t *testing.T) {
		dir := newDataDir(t)
		for _, args := range [][]string{
			{"check", "--nope"},
			{"check", "extra", "--data-dir", dir},
			{"check", "--data-dir", "relative/dir"},
		} {
			if code, _, _ := zatvor(t, "", args...); code != 2 {
				t.Errorf("zatvor %q = %d, want 2", args, code)
			}
		}
	})
}

func TestStatusJSONStable(t *testing.T) {
	state := `{"phase":"connected","profile":"p1","ports":{"Mixed":31001,"Probe":33101,"DevTools":35201,"API":37301},
	"check":{"status":"ok","ipv4":"203.0.113.9","at":"2026-11-22T10:00:00Z","gen":2},
	"warnings":[{"code":"proxy_drift","message":"another program changed the system proxy"}],"safe":true,"futureField":1}`
	profiles := `{"profiles":[{"id":"p0","name":"other"},{"id":"p1","name":"Frankfurt","subscription":"s1","kind":"link","protocol":"vless","routing":"preset","disabled":false}]}`
	f := newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/status":   serve(200, state),
		"GET /v1/profiles": serve(200, profiles),
	})
	code, out, errOut := zatvor(t, f.token, "status", "--json", "--data-dir", f.dataDir)
	golden := `{
  "phase": "connected",
  "profile": {
    "id": "p1",
    "name": "Frankfurt"
  },
  "ports": {
    "mixed": 31001,
    "probe": 33101,
    "devTools": 35201,
    "api": 37301
  },
  "check": {
    "status": "ok",
    "ipv4": "203.0.113.9",
    "ipv6": "",
    "at": "2026-11-22T10:00:00Z"
  },
  "warnings": [
    {
      "code": "proxy_drift",
      "message": "another program changed the system proxy"
    }
  ],
  "error": null,
  "safe": true,
  "geoBootstrap": false
}
`
	if code != 0 || out != golden {
		t.Errorf("status --json = %d (stderr %q)\n%s\nwant\n%s", code, errOut, out, golden)
	}

	// Disconnected, nothing checked yet: the same keys, empty values.
	f = newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/status":   serve(200, `{"phase":"failed","ports":{"Mixed":0,"Probe":0,"DevTools":0,"API":0},"check":{"status":"","at":"0001-01-01T00:00:00Z","gen":0},"error":{"code":"xray_exited","message":"Xray exited"},"safe":false}`),
		"GET /v1/profiles": serve(200, `{"profiles":[]}`),
	})
	code, out, _ = zatvor(t, f.token, "status", "--json", "--data-dir", f.dataDir)
	golden = `{
  "phase": "failed",
  "profile": null,
  "ports": {
    "mixed": 0,
    "probe": 0,
    "devTools": 0,
    "api": 0
  },
  "check": {
    "status": "",
    "ipv4": "",
    "ipv6": "",
    "at": null
  },
  "warnings": [],
  "error": {
    "code": "xray_exited",
    "message": "Xray exited"
  },
  "safe": false,
  "geoBootstrap": false
}
`
	if code != 0 || out != golden {
		t.Errorf("status --json (failed) = %d\n%s\nwant\n%s", code, out, golden)
	}
}

func TestStatusHuman(t *testing.T) {
	f := newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/status": serve(200, `{"phase":"connected","profile":"p1","ports":{"Mixed":31001,"Probe":33101,"DevTools":35201,"API":37301},
		"check":{"status":"ok","ipv4":"203.0.113.9","at":"2026-11-22T10:00:00Z","gen":2},"warnings":[{"code":"proxy_drift","message":"m"}],"safe":true}`),
		"GET /v1/profiles": serve(200, `{"profiles":[{"id":"p1","name":"Frankfurt"}]}`),
	})
	code, out, _ := zatvor(t, f.token, "status", "--data-dir", f.dataDir)
	for _, want := range []string{"connected", "safe mode", "Frankfurt", "31001", "ok", "203.0.113.9", "proxy_drift"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
	if code != 0 {
		t.Errorf("status = %d", code)
	}
	if code, _, _ := zatvor(t, "", "status", "--data-dir", newDataDir(t)); code != 3 {
		t.Errorf("status without a daemon = %d, want 3", code)
	}
	if code, _, _ := zatvor(t, "", "status", "--bogus"); code != 2 {
		t.Errorf("status --bogus = %d, want 2", code)
	}
}

func TestNoRedirectsFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		writeJSON(w, 200, `{"phase":"connected"}`)
	}))
	defer other.Close()
	f := newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/status": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/v1/status", http.StatusFound)
		},
	})
	if code, _, _ := zatvor(t, f.token, "status", "--data-dir", f.dataDir); code != 1 || elsewhere.Load() != 0 {
		t.Errorf("status = %d with %d requests elsewhere, want 1 and none", code, elsewhere.Load())
	}
}

func TestDiagWritesAtomically(t *testing.T) {
	var body atomic.Value
	body.Store("PK first zip")
	fail := atomic.Bool{}
	f := newFakeAPI(t, map[string]http.HandlerFunc{
		"GET /v1/diag": func(w http.ResponseWriter, _ *http.Request) {
			if fail.Load() {
				writeJSON(w, 500, `{"code":"internal","message":"internal error"}`)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Write([]byte(body.Load().(string)))
		},
	})
	outDir := t.TempDir()
	out := filepath.Join(outDir, "bundle.zip")
	entries := func() []string {
		es, _ := os.ReadDir(outDir)
		var names []string
		for _, e := range es {
			names = append(names, e.Name())
		}
		return names
	}

	if code, stdout, errOut := zatvor(t, f.token, "diag", "-o", out, "--data-dir", f.dataDir); code != 0 {
		t.Fatalf("diag = %d (%q, %q)", code, stdout, errOut)
	}
	if b, _ := os.ReadFile(out); string(b) != "PK first zip" {
		t.Errorf("bundle = %q", b)
	}
	if fmt.Sprint(entries()) != "[bundle.zip]" {
		t.Errorf("directory holds %q, want only the bundle", entries())
	}

	body.Store("PK second zip")
	hits := f.hits.Load()
	if code, _, errOut := zatvor(t, f.token, "diag", "-o", out, "--data-dir", f.dataDir); code != 1 || !strings.Contains(errOut, "--force") {
		t.Errorf("diag over an existing file = %d (%q), want 1 naming --force", code, errOut)
	}
	if b, _ := os.ReadFile(out); string(b) != "PK first zip" || f.hits.Load() != hits {
		t.Errorf("refused diag changed the file (%q) or asked the daemon", b)
	}
	if code, _, _ := zatvor(t, f.token, "diag", "--force", "-o", out, "--data-dir", f.dataDir); code != 0 {
		t.Errorf("diag --force = %d", code)
	}
	if b, _ := os.ReadFile(out); string(b) != "PK second zip" {
		t.Errorf("bundle after --force = %q", b)
	}

	fail.Store(true)
	other := filepath.Join(outDir, "failed.zip")
	if code, _, errOut := zatvor(t, f.token, "diag", "-o", other, "--data-dir", f.dataDir); code != 1 || !strings.Contains(errOut, "internal") {
		t.Errorf("failing diag = %d (%q), want 1", code, errOut)
	}
	if fmt.Sprint(entries()) != "[bundle.zip]" {
		t.Errorf("a failed diag left %q", entries())
	}

	if code, _, _ := zatvor(t, "", "diag", "-o", out, "--data-dir", newDataDir(t), "--force"); code != 3 {
		t.Errorf("diag without a daemon = %d, want 3", code)
	}
	if code, _, _ := zatvor(t, "", "diag", "--data-dir", f.dataDir); code != 2 {
		t.Errorf("diag without -o = %d, want 2", code)
	}
}

func TestHelpDocumentsExitCodes(t *testing.T) {
	code, out, _ := zatvor(t, "", "--help")
	if code != 0 {
		t.Errorf("--help = %d", code)
	}
	for _, want := range []string{"status", "check", "diag", "--data-dir", "Exit codes", "0 ", "1 ", "2 ", "3 "} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	if code, _, errOut := zatvor(t, ""); code != 2 || !strings.Contains(errOut, "Usage") {
		t.Errorf("no command = %d (%q), want 2 with usage", code, errOut)
	}
	if code, _, _ := zatvor(t, "", "frobnicate"); code != 2 {
		t.Errorf("unknown command = %d, want 2", code)
	}
}
