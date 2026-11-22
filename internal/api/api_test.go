package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/devtools"
	"github.com/ArtikGenius/zatvor/internal/engine"
	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/settings"
	"github.com/ArtikGenius/zatvor/internal/subsvc"
)

// Secret-looking test values. None is real; each must never leave the daemon.
const (
	subURL     = "https://sub.example.org/api/v1/client/subscribe?token=fake-sub-tok-0001"
	subPath    = "/api/v1/client/subscribe"
	subTok     = "fake-sub-tok-0001"
	nodeID     = "00000000-0000-0000-0000-00000000abcd"
	nodePass   = "fake-node-pass-0002"
	nodeServer = "node.example.net"
)

// fakeBackend is an in-memory api.Backend.
type fakeBackend struct {
	mu        sync.Mutex
	state     engine.State
	subs      []profile.Subscription
	profiles  []profile.Profile
	set       settings.Settings
	err       error // returned by every mutating call when set
	connected string
	added     [2]string
	updated   string
	removed   string
	devtools  *bool
	force     *bool
	shows     int
	listeners map[chan api.Event]struct{}
}

func newFake() *fakeBackend {
	return &fakeBackend{
		state:     engine.State{Phase: engine.Disconnected},
		set:       settings.Defaults(),
		listeners: map[chan api.Event]struct{}{},
	}
}

func (f *fakeBackend) Status() engine.State { f.mu.Lock(); defer f.mu.Unlock(); return f.state }

func (f *fakeBackend) Subscribe(ctx context.Context) <-chan api.Event {
	ch := make(chan api.Event, 16)
	f.mu.Lock()
	f.listeners[ch] = struct{}{}
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		delete(f.listeners, ch)
		close(ch)
		f.mu.Unlock()
	}()
	return ch
}

func (f *fakeBackend) emit(ev api.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.listeners {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (f *fakeBackend) listenerCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.listeners) }

func (f *fakeBackend) Profiles() []profile.Profile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profiles
}

func (f *fakeBackend) Connect(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = id
	if f.err != nil {
		return f.err
	}
	f.state = engine.State{Phase: engine.Connected, Profile: id}
	return nil
}

func (f *fakeBackend) Disconnect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.state = engine.State{Phase: engine.Disconnected}
	return nil
}

func (f *fakeBackend) ResolveConflict(_ context.Context, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.force = &force
	return f.err
}

func (f *fakeBackend) Subscriptions() []profile.Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs
}

func (f *fakeBackend) AddSubscription(_ context.Context, rawURL, ua string) (profile.Subscription, profile.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = [2]string{rawURL, ua}
	if f.err != nil {
		return profile.Subscription{}, profile.Diff{}, f.err
	}
	s := profile.Subscription{ID: "s1", URL: rawURL, UA: ua, SendDevice: true}
	return s, profile.Diff{Added: []string{"p1"}}, nil
}

func (f *fakeBackend) UpdateSubscription(_ context.Context, id string) (profile.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = id
	if f.err != nil {
		return profile.Diff{}, f.err
	}
	return profile.Diff{Changed: []string{"p1"}}, nil
}

func (f *fakeBackend) RemoveSubscription(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = id
	return f.err
}

func (f *fakeBackend) Settings() settings.Settings { f.mu.Lock(); defer f.mu.Unlock(); return f.set }

func (f *fakeBackend) PutSettings(s settings.Settings) (settings.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return settings.Settings{}, f.err
	}
	f.set = s
	return s, nil
}

func (f *fakeBackend) SetDevTools(on bool) ([]devtools.Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devtools = &on
	if f.err != nil {
		return nil, f.err
	}
	st := devtools.Off
	if on {
		st = devtools.On
	}
	return []devtools.Target{{Name: "claude", Path: "/x/settings.json", State: st}}, nil
}

func (f *fakeBackend) Show() { f.mu.Lock(); f.shows++; f.mu.Unlock() }

func (f *fakeBackend) setErr(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

// env is one running server.
type env struct {
	t     *testing.T
	srv   *api.Server
	dirs  appdir.Dirs
	addr  string
	token string
	fake  *fakeBackend
}

func startServer(t *testing.T, f *fakeBackend) *env {
	t.Helper()
	dirs, err := appdir.Resolve(filepath.Join(t.TempDir(), "Zatvor"))
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(f, dirs)
	addr, err := srv.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	tok, err := os.ReadFile(filepath.Join(dirs.Run, api.TokenFile))
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, srv: srv, dirs: dirs, addr: addr, token: string(tok), fake: f}
}

var client = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}

// req sends a request with the right token and Host unless mod changes them.
func (e *env) req(method, path, body string, mod func(*http.Request)) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, "http://"+e.addr+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+e.token)
	if mod != nil {
		mod(r)
	}
	resp, err := client.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		e.t.Errorf("%s %s: Content-Type %q, want JSON", method, path, ct)
	}
	return resp.StatusCode, string(b)
}

func wantForbidden(t *testing.T, what string, code int, body string) {
	t.Helper()
	if code != http.StatusForbidden {
		t.Errorf("%s: status %d, want 403 (body %s)", what, code, body)
		return
	}
	if strings.TrimSpace(body) != `{"code":"forbidden","message":"forbidden"}` {
		t.Errorf("%s: body %s, want the bare forbidden error", what, body)
	}
}

func TestRejectsMissingToken(t *testing.T) {
	e := startServer(t, newFake())
	code, body := e.req("GET", "/v1/status", "", func(r *http.Request) { r.Header.Del("Authorization") })
	wantForbidden(t, "no token", code, body)
	code, body = e.req("GET", "/v1/status", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("0", 64)) })
	wantForbidden(t, "wrong token", code, body)
	code, body = e.req("GET", "/v1/status", "", func(r *http.Request) { r.Header.Set("Authorization", e.token) })
	wantForbidden(t, "token without Bearer", code, body)
	code, body = e.req("GET", "/v1/status", "", func(r *http.Request) { r.Header.Add("Authorization", "Bearer x") })
	wantForbidden(t, "two Authorization headers", code, body)
	code, body = e.req("POST", "/v1/disconnect", "", func(r *http.Request) { r.Header.Del("Authorization") })
	wantForbidden(t, "mutating call without token", code, body)
	if code, body := e.req("GET", "/v1/status", "", nil); code != 200 {
		t.Errorf("with token: status %d (%s)", code, body)
	}
}

func TestRejectsWrongHost(t *testing.T) {
	e := startServer(t, newFake())
	_, port, _ := net.SplitHostPort(e.addr)
	for _, host := range []string{
		"localhost:" + port,
		"evil.example:" + port,
		"127.0.0.1",
		"127.0.0.1:1",
		"[::1]:" + port,
		"127.0.0.1:" + port + ".evil.example",
	} {
		code, body := e.req("GET", "/v1/status", "", func(r *http.Request) { r.Host = host })
		wantForbidden(t, "Host "+host, code, body)
	}
	// No Host at all (HTTP/1.0) and an empty one; net/http cannot send these.
	for _, raw := range []string{
		"GET /v1/status HTTP/1.0\r\nAuthorization: Bearer " + e.token + "\r\n\r\n",
		"GET /v1/status HTTP/1.1\r\nHost: \r\nAuthorization: Bearer " + e.token + "\r\nConnection: close\r\n\r\n",
	} {
		c, err := net.Dial("tcp", e.addr)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(c, raw)
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		c.Close()
		wantForbidden(t, fmt.Sprintf("raw %q", strings.SplitN(raw, "\r\n", 2)[0]), resp.StatusCode, string(b))
	}
}

func TestRejectsForeignOrigin(t *testing.T) {
	e := startServer(t, newFake())
	for _, o := range []string{"http://evil.example", "http://" + e.addr, "file://", "chrome-extension://abc"} {
		code, body := e.req("GET", "/v1/status", "", func(r *http.Request) { r.Header.Set("Origin", o) })
		wantForbidden(t, "Origin "+o, code, body)
	}
	code, body := e.req("GET", "/v1/status", "", func(r *http.Request) {
		r.Header.Add("Origin", "null")
		r.Header.Add("Origin", "http://evil.example")
	})
	wantForbidden(t, "two Origin headers", code, body)
	code, body = e.req("OPTIONS", "/v1/connect", "", func(r *http.Request) {
		r.Header.Del("Authorization")
		r.Header.Set("Origin", "http://evil.example")
		r.Header.Set("Access-Control-Request-Method", "POST")
	})
	wantForbidden(t, "CORS preflight", code, body)
	for _, o := range []string{"", "null"} {
		code, body := e.req("GET", "/v1/status", "", func(r *http.Request) {
			if o != "" {
				r.Header.Set("Origin", o)
			}
		})
		if code != 200 {
			t.Errorf("Origin %q: status %d (%s)", o, code, body)
		}
	}
}

func TestTokenFileUserOnly(t *testing.T) {
	e := startServer(t, newFake())
	if len(e.token) != 64 || strings.Trim(e.token, "0123456789abcdef") != "" {
		t.Errorf("token is %d chars, want 64 lowercase hex", len(e.token))
	}
	got, err := api.ReadToken(e.dirs)
	if err != nil || got != e.token {
		t.Errorf("ReadToken = %q, %v", got, err)
	}
	for _, name := range []string{api.TokenFile, api.DiscoveryFile} {
		assertUserOnly(t, filepath.Join(e.dirs.Run, name))
	}
	other := startServer(t, newFake())
	if other.token == e.token {
		t.Error("two servers got the same token")
	}
}

func TestDiscoveryRemovedOnStop(t *testing.T) {
	e := startServer(t, newFake())
	raw, err := os.ReadFile(filepath.Join(e.dirs.Run, api.DiscoveryFile))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(e.addr)
	if len(d) != 3 || d["version"] != 1.0 || fmt.Sprint(d["port"]) != port || d["pid"] != float64(os.Getpid()) {
		t.Errorf("api.json = %s, want version 1, port %s, pid %d", raw, port, os.Getpid())
	}
	disc, err := api.ReadDiscovery(e.dirs)
	if err != nil || fmt.Sprint(disc.Port) != port {
		t.Errorf("ReadDiscovery = %+v, %v", disc, err)
	}
	e.srv.Stop()
	for _, name := range []string{api.TokenFile, api.DiscoveryFile} {
		if _, err := os.Stat(filepath.Join(e.dirs.Run, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still there after Stop (%v)", name, err)
		}
	}
	if _, err := client.Get("http://" + e.addr + "/v1/status"); err == nil {
		t.Error("server still answers after Stop")
	}
	e.srv.Stop() // idempotent
}

func TestStopKeepsForeignDiscovery(t *testing.T) {
	e := startServer(t, newFake())
	// Another instance took over the files (it would have the lock; this
	// simulates a stale Stop racing a fresh start).
	foreign := `{"version":1,"port":1,"pid":1}`
	if err := os.WriteFile(filepath.Join(e.dirs.Run, api.DiscoveryFile), []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dirs.Run, api.TokenFile), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.Stop()
	for _, name := range []string{api.TokenFile, api.DiscoveryFile} {
		if _, err := os.Stat(filepath.Join(e.dirs.Run, name)); err != nil {
			t.Errorf("Stop removed the other instance's %s: %v", name, err)
		}
	}
}

// freePort returns a loopback port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPersistedPortUsedWhenFree(t *testing.T) {
	port := freePort(t)
	f := newFake()
	f.set.LocalAPIPort = port
	e := startServer(t, f)
	if e.addr != fmt.Sprintf("127.0.0.1:%d", port) {
		t.Errorf("addr %s, want the persisted port %d", e.addr, port)
	}
}

// wantFreshPort checks that e listens on 127.0.0.1 at a port other than not.
func wantFreshPort(t *testing.T, e *env, not int) {
	t.Helper()
	host, p, _ := net.SplitHostPort(e.addr)
	if host != "127.0.0.1" || p == fmt.Sprint(not) || p == "0" {
		t.Errorf("addr %s, want 127.0.0.1 with a fresh port (not %d)", e.addr, not)
	}
	if code, body := e.req("GET", "/v1/status", "", nil); code != 200 {
		t.Errorf("status %d (%s)", code, body)
	}
}

func TestPersistedPortTakenFallsBack(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	f := newFake()
	f.set.LocalAPIPort = port
	wantFreshPort(t, startServer(t, f), port)
}

func TestPersistedPortNeverAnXrayPort(t *testing.T) {
	for _, slot := range []string{"Mixed", "Probe", "DevTools", "API"} {
		port := freePort(t) // free, so only the collision rule can refuse it
		f := newFake()
		f.set.LocalAPIPort = port
		switch slot {
		case "Mixed":
			f.set.Ports.Mixed = port
		case "Probe":
			f.set.Ports.Probe = port
		case "DevTools":
			f.set.Ports.DevTools = port
		case "API":
			f.set.Ports.API = port
		}
		t.Run(slot, func(t *testing.T) { wantFreshPort(t, startServer(t, f), port) })
	}
	// Xray's api inbound port alone is never taken as ours.
	port := freePort(t)
	f := newFake()
	f.set.Ports.API = port
	wantFreshPort(t, startServer(t, f), port)
}

// sseReader reads named events from an event stream.
type sseReader struct{ sc *bufio.Scanner }

func (s *sseReader) next(t *testing.T) (name, data string) {
	t.Helper()
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			if name != "" || data != "" {
				return name, data
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data += strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("stream ended: %v", s.sc.Err())
	return "", ""
}

func TestSSEDeliversStateChange(t *testing.T) {
	f := newFake()
	e := startServer(t, f)
	r, _ := http.NewRequest("GET", "http://"+e.addr+"/v1/events", nil)
	r.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sr := &sseReader{bufio.NewScanner(resp.Body)}
	name, data := sr.next(t)
	if name != "state" || !strings.Contains(data, `"phase":"disconnected"`) {
		t.Errorf("first event %s %s, want the current state", name, data)
	}

	f.emit(api.StateEvent(engine.State{Phase: engine.Connected, Profile: "p1"}))
	name, data = sr.next(t)
	var st engine.State
	if name != "state" || json.Unmarshal([]byte(data), &st) != nil || st.Phase != engine.Connected || st.Profile != "p1" {
		t.Errorf("got %s %s, want state connected p1", name, data)
	}

	f.emit(api.SubscriptionEvent(subsvc.Event{Kind: subsvc.Failed, ID: "s1", Code: subsvc.CodeDeviceLimit}))
	name, data = sr.next(t)
	if name != "subscription" || data != `{"id":"s1","result":"failed","code":"device_limit","diff":{"added":[],"removed":[],"changed":[],"warnings":[]}}` {
		t.Errorf("got %s %s", name, data)
	}
	f.emit(api.SubscriptionEvent(subsvc.Event{Kind: subsvc.Updated, ID: "s1", Diff: profile.Diff{Added: []string{"p2"}}}))
	name, data = sr.next(t)
	if name != "subscription" || data != `{"id":"s1","result":"updated","diff":{"added":["p2"],"removed":[],"changed":[],"warnings":[]}}` {
		t.Errorf("got %s %s", name, data)
	}

	f.emit(api.WarningEvent(engine.Error{Code: engine.WarnProxyDrift, Message: "system proxy changed"}))
	name, data = sr.next(t)
	if name != "warning" || data != `{"code":"proxy_drift","message":"system proxy changed"}` {
		t.Errorf("got %s %s", name, data)
	}

	// Stop ends the stream and the subscription.
	e.srv.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for f.listenerCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := f.listenerCount(); n != 0 {
		t.Errorf("%d subscriptions still open after Stop", n)
	}
}

func TestErrorBodiesCarryNoSecret(t *testing.T) {
	f := newFake()
	e := startServer(t, f)
	tests := []struct {
		name       string
		err        error
		method     string
		path, body string
		status     int
		want       string
	}{
		{"wrapped subsvc error", fmt.Errorf("fetch %s: %w", subURL, &subsvc.Error{Code: subsvc.CodeNetwork, Message: "the provider did not answer"}),
			"POST", "/v1/subscriptions", `{"url":"` + subURL + `"}`, 422, `{"code":"network","message":"the provider did not answer"}`},
		{"subsvc not found", &subsvc.Error{Code: subsvc.CodeNotFound, Message: "no such subscription"},
			"POST", "/v1/subscriptions/s9/update", "", 404, `{"code":"not_found","message":"no such subscription"}`},
		{"subsvc invalid url", &subsvc.Error{Code: subsvc.CodeInvalidURL, Message: "not an http(s) URL"},
			"POST", "/v1/subscriptions", `{"url":"x"}`, 400, `{"code":"invalid_url","message":"not an http(s) URL"}`},
		{"wrapped engine conflict", fmt.Errorf("connect via %s: %w", subURL, &engine.Error{Code: engine.CodeProxyConflict, Message: "a foreign proxy is set", Conflict: &engine.Conflict{PAC: true}}),
			"POST", "/v1/connect", `{"profile":"p1"}`, 409, `{"code":"proxy_conflict","message":"a foreign proxy is set"}`},
		{"engine no conflict", &engine.Error{Code: engine.CodeNoConflict, Message: "no conflict pending"},
			"POST", "/v1/proxy/resolve-conflict", `{"force":true}`, 409, `{"code":"no_conflict","message":"no conflict pending"}`},
		{"devtools error with a path", &devtools.Error{Code: devtools.CodeConflict, Target: "claude", Err: fmt.Errorf("/home/u/%s/settings.json: %w", subTok, devtools.ErrChanged)},
			"POST", "/v1/devtools", `{"on":true}`, 409, `{"code":"devtools_conflict","message":"devtools: claude: devtools_conflict"}`},
		{"joined devtools errors", errors.Join(&devtools.Error{Code: devtools.CodeIO, Target: "vscode", Err: errors.New(subURL)}),
			"POST", "/v1/devtools", `{"on":false}`, 422, `{"code":"devtools_io","message":"devtools: vscode: devtools_io"}`},
		{"settings refused", fmt.Errorf("save %s: %w", subTok, settings.ErrRefuseEmpty),
			"PUT", "/v1/settings", `{"version":1}`, 400, `{"code":"settings_empty","message":"refusing to overwrite the settings with empty ones"}`},
		{"api error", fmt.Errorf("%s: %w", subURL, &api.Error{Code: api.CodeNotFound, Message: "no such profile"}),
			"POST", "/v1/connect", `{"profile":"zz"}`, 404, `{"code":"not_found","message":"no such profile"}`},
		{"unknown error", fmt.Errorf("dial %s: connection refused", subURL),
			"DELETE", "/v1/subscriptions/s1", "", 500, `{"code":"internal","message":"internal error"}`},
	}
	for _, tc := range tests {
		f.setErr(tc.err)
		code, body := e.req(tc.method, tc.path, tc.body, nil)
		if code != tc.status || strings.TrimSpace(body) != tc.want {
			t.Errorf("%s: %d %s, want %d %s", tc.name, code, body, tc.status, tc.want)
		}
		for _, s := range []string{subURL, subTok, subPath, "/home/u"} {
			if strings.Contains(body, s) {
				t.Errorf("%s: body leaks %q", tc.name, s)
			}
		}
	}
}

func TestSubscriptionsListRedacted(t *testing.T) {
	f := newFake()
	node := &link.Node{Scheme: "vless", Remark: "alpha", Address: nodeServer, Port: 443,
		Params: map[string]string{"id": nodeID, "password": nodePass, "security": "reality"},
		Raw:    "vless://" + nodeID + "@" + nodeServer + ":443?security=reality#alpha"}
	p := profile.Profile{ID: "p1", Name: "alpha", SubscriptionID: "s1", Node: node,
		RoutingSource: profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllExceptRU}}
	f.subs = []profile.Subscription{{
		ID: "s1", URL: subURL, Title: "My provider", UA: "zatvor/1", SendDevice: true,
		Profiles: []profile.Profile{p},
	}, {
		ID: "s2", URL: "user:" + nodePass + "@sub2.example.org/" + subTok, Title: "Second",
	}}
	f.profiles = []profile.Profile{p}
	e := startServer(t, f)

	code, body := e.req("GET", "/v1/subscriptions", "", nil)
	if code != 200 {
		t.Fatalf("status %d (%s)", code, body)
	}
	var got struct {
		Subscriptions []api.SubscriptionInfo `json:"subscriptions"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Subscriptions) != 2 || got.Subscriptions[0].Host != "sub.example.org" || got.Subscriptions[0].Profiles != 1 ||
		got.Subscriptions[0].Title != "My provider" || !got.Subscriptions[0].SendDevice || got.Subscriptions[1].Host != "sub2.example.org" {
		t.Errorf("subscriptions = %+v", got.Subscriptions)
	}

	code, pbody := e.req("GET", "/v1/profiles", "", nil)
	if code != 200 {
		t.Fatalf("profiles: status %d (%s)", code, pbody)
	}
	if want := `{"profiles":[{"id":"p1","name":"alpha","subscription":"s1","kind":"link","protocol":"vless","routing":"preset","disabled":false}]}`; strings.TrimSpace(pbody) != want {
		t.Errorf("profiles = %s, want %s", pbody, want)
	}

	code, abody := e.req("POST", "/v1/subscriptions", `{"url":"`+subURL+`","ua":"ua/2"}`, nil)
	if code != 200 || !strings.Contains(abody, `"host":"sub.example.org"`) || !strings.Contains(abody, `"added":["p1"]`) {
		t.Errorf("add: %d %s", code, abody)
	}
	if f.added != [2]string{subURL, "ua/2"} {
		t.Errorf("backend got %q", f.added)
	}

	for _, b := range []string{body, pbody, abody} {
		for _, s := range []string{subTok, subPath, nodeID, nodePass, nodeServer, "token="} {
			if strings.Contains(b, s) {
				t.Errorf("response leaks %q: %s", s, b)
			}
		}
	}
}

func TestBodyLimit(t *testing.T) {
	f := newFake()
	e := startServer(t, f)
	big := `{"url":"https://x.example/","ua":"` + strings.Repeat("a", api.MaxBody) + `"}`
	code, body := e.req("POST", "/v1/subscriptions", big, nil)
	if code != http.StatusRequestEntityTooLarge || strings.TrimSpace(body) != `{"code":"body_too_large","message":"request body over 65536 bytes"}` {
		t.Errorf("with Content-Length: %d %s", code, body)
	}
	// Chunked, so the length is only known while reading.
	code, body = e.req("POST", "/v1/subscriptions", "", func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(big))
		r.ContentLength = -1
	})
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked: %d %s", code, body)
	}
	if f.added != [2]string{} {
		t.Errorf("backend was called with an oversized body")
	}
	// Just under the limit is fine.
	ok := `{"url":"https://x.example/","ua":"` + strings.Repeat("a", api.MaxBody-64) + `"}`
	if code, body := e.req("POST", "/v1/subscriptions", ok, nil); code != 200 {
		t.Errorf("under the limit: %d %.200s", code, body)
	}
}

func TestBadRequests(t *testing.T) {
	e := startServer(t, newFake())
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/connect", `{"profile":`},
		{"POST", "/v1/connect", `{"profile":""}`},
		{"POST", "/v1/connect", `{"profile":"p1"} {"profile":"p2"}`},
		{"POST", "/v1/subscriptions", `{"ua":"x"}`},
		{"POST", "/v1/devtools", `{}`},
		{"POST", "/v1/proxy/resolve-conflict", `{}`},
		{"PUT", "/v1/settings", `[]`},
	} {
		code, body := e.req(tc.method, tc.path, tc.body, nil)
		if code != 400 || !strings.HasPrefix(body, `{"code":"bad_request","message":`) {
			t.Errorf("%s %s %s: %d %s", tc.method, tc.path, tc.body, code, body)
		}
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/v1/nope"}, {"GET", "/"}, {"DELETE", "/v1/status"}} {
		code, body := e.req(tc.method, tc.path, "", nil)
		if code != 404 || strings.TrimSpace(body) != `{"code":"not_found","message":"no such endpoint"}` {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, code, body)
		}
	}
}

func TestRoutesReachBackend(t *testing.T) {
	f := newFake()
	e := startServer(t, f)
	check := func(method, path, body, want string) {
		t.Helper()
		code, got := e.req(method, path, body, nil)
		if code != 200 || strings.TrimSpace(got) != want {
			t.Errorf("%s %s: %d %s, want %s", method, path, code, got, want)
		}
	}
	check("POST", "/v1/connect", `{"profile":"p1"}`, `{"phase":"connected","profile":"p1","ports":{"Mixed":0,"Probe":0,"DevTools":0,"API":0},"check":{"status":"","at":"0001-01-01T00:00:00Z","gen":0},"safe":false}`)
	check("GET", "/v1/status", "", `{"phase":"connected","profile":"p1","ports":{"Mixed":0,"Probe":0,"DevTools":0,"API":0},"check":{"status":"","at":"0001-01-01T00:00:00Z","gen":0},"safe":false}`)
	check("POST", "/v1/disconnect", "", `{"phase":"disconnected","ports":{"Mixed":0,"Probe":0,"DevTools":0,"API":0},"check":{"status":"","at":"0001-01-01T00:00:00Z","gen":0},"safe":false}`)
	check("POST", "/v1/proxy/resolve-conflict", `{"force":false}`, `{"phase":"disconnected","ports":{"Mixed":0,"Probe":0,"DevTools":0,"API":0},"check":{"status":"","at":"0001-01-01T00:00:00Z","gen":0},"safe":false}`)
	check("POST", "/v1/subscriptions/s7/update", "", `{"diff":{"added":[],"removed":[],"changed":["p1"],"warnings":[]}}`)
	check("DELETE", "/v1/subscriptions/s8", "", `{"ok":true}`)
	check("POST", "/v1/devtools", `{"on":true}`, `{"targets":[{"name":"claude","path":"/x/settings.json","state":"on"}]}`)
	check("PUT", "/v1/settings", `{"version":1,"ports":{"Mixed":1,"Probe":2,"DevTools":3,"API":4},"logLevel":"info"}`,
		`{"version":1,"ports":{"Mixed":1,"Probe":2,"DevTools":3,"API":4},"activeProfile":"","devTools":false,"logLevel":"info","updateOnStart":false,"localApiPort":0}`)
	check("POST", "/v1/show", "", `{"ok":true}`)
	if f.connected != "p1" || f.updated != "s7" || f.removed != "s8" || f.devtools == nil || !*f.devtools ||
		f.force == nil || *f.force || f.shows != 1 || f.set.LogLevel != "info" {
		t.Errorf("backend saw connect %q update %q remove %q devtools %v force %v shows %d settings %+v",
			f.connected, f.updated, f.removed, f.devtools, f.force, f.shows, f.set)
	}
}

func TestStartTwiceFails(t *testing.T) {
	e := startServer(t, newFake())
	if _, err := e.srv.Start(context.Background()); err == nil {
		t.Error("second Start succeeded")
	}
}
