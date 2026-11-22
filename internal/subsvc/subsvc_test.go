package subsvc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/hwid"
	"github.com/ArtikGenius/zatvor/internal/profile"
	"github.com/ArtikGenius/zatvor/internal/subfetch"
)

const (
	nodeA = "vless://00000000-0000-4000-8000-000000000000@a.example.com:443?security=tls#alpha\n"
	nodeB = "vless://00000000-0000-4000-8000-000000000000@b.example.com:443?security=tls#beta\n"
)

type memStore struct {
	mu    sync.Mutex
	subs  []profile.Subscription
	saves int
}

func (m *memStore) Load() ([]profile.Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]profile.Subscription(nil), m.subs...), nil
}

func (m *memStore) Save(s []profile.Subscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	m.subs = append([]profile.Subscription(nil), s...)
	return nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) After(time.Duration) <-chan time.Time {
	return make(chan time.Time) // never fires; tests drive runDue directly
}
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type fakeTunnel struct {
	up   atomic.Bool
	dial subfetch.Dialer
}

func (f *fakeTunnel) ProbeDialer() (subfetch.Dialer, bool) {
	if !f.up.Load() {
		return nil, false
	}
	return f.dial, true
}

type provider struct {
	*httptest.Server
	hits    atomic.Int32
	status  atomic.Int32
	body    atomic.Value // string
	headers atomic.Value // http.Header of the last request
	extra   atomic.Value // http.Header added to responses
}

func newProvider(t *testing.T, body string) *provider {
	p := &provider{}
	p.status.Store(200)
	p.body.Store(body)
	p.extra.Store(http.Header{})
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		p.headers.Store(r.Header.Clone())
		for k, v := range p.extra.Load().(http.Header) {
			w.Header()[k] = v
		}
		w.WriteHeader(int(p.status.Load()))
		_, _ = w.Write([]byte(p.body.Load().(string)))
	}))
	t.Cleanup(p.Close)
	return p
}

type env struct {
	svc   *Service
	st    *memStore
	clk   *fakeClock
	tun   *fakeTunnel
	fetch Fetcher
}

func newEnv(t *testing.T, mut func(*Deps)) *env {
	e := &env{st: &memStore{}, clk: &fakeClock{t: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}, tun: &fakeTunnel{}}
	e.fetch = subfetch.New(5 * time.Second)
	d := Deps{
		Fetcher: e.fetch, Store: e.st, Tunnel: e.tun, Clock: e.clk,
		MachineID: "machine-secret-id", Device: hwid.Device{OS: "windows"},
		Version: "1.0.0", OS: "windows",
		BuildCheck: func(profile.Profile) error { return nil },
	}
	if mut != nil {
		mut(&d)
	}
	e.svc = New(d)
	return e
}

func (e *env) seed(t *testing.T, url string, iv time.Duration) profile.Subscription {
	t.Helper()
	sub, _, err := e.svc.Add(context.Background(), url, "")
	if err != nil {
		t.Fatal(err)
	}
	subs, _ := e.st.Load()
	subs[0].Info.UpdateInterval = iv
	subs[0].URL = url
	_ = e.st.Save(subs)
	e.st.saves = 0
	return sub
}

func TestAddDefaultsSendDevice(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub, diff, err := e.svc.Add(context.Background(), p.URL+"/sub?token=abc", "")
	if err != nil {
		t.Fatal(err)
	}
	if !sub.SendDevice || sub.ID == "" || len(sub.ID) != 16 || len(diff.Added) != 1 {
		t.Fatalf("%+v %+v", sub, diff)
	}
	stored, _ := e.st.Load()
	if len(stored) != 1 || !stored[0].SendDevice || stored[0].UA != "Zatvor/1.0.0 (windows)" {
		t.Fatalf("%+v", stored)
	}
	if p.hits.Load() != 1 {
		t.Fatalf("hits %d", p.hits.Load())
	}
}

func TestScheduledSkipsWithoutTunnel(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	e.seed(t, p.URL+"/s", time.Hour)
	p.hits.Store(0)
	e.clk.advance(30 * time.Hour)
	e.svc.runDue(context.Background())
	e.svc.runDue(context.Background())
	if p.hits.Load() != 0 {
		t.Fatalf("scheduled update sent %d requests without a tunnel", p.hits.Load())
	}
	subs, _ := e.st.Load()
	_, err := e.svc.Update(context.Background(), subs[0].ID, Scheduled)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeNoTunnel || p.hits.Load() != 0 {
		t.Fatalf("%v hits=%d", err, p.hits.Load())
	}
	// no backoff growth: once the tunnel is up the update runs at once
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	e.svc.runDue(context.Background())
	if p.hits.Load() != 1 {
		t.Fatalf("hits %d", p.hits.Load())
	}
}

func TestUserUpdateWithoutTunnelIsOneDirectRequest(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub := e.seed(t, p.URL+"/s", 0)
	p.hits.Store(0)
	if _, err := e.svc.Update(context.Background(), sub.ID, User); err != nil {
		t.Fatal(err)
	}
	if p.hits.Load() != 1 {
		t.Fatalf("hits %d", p.hits.Load())
	}
}

func TestScheduledBackoffCapped(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	e.seed(t, p.URL+"/s", time.Hour)
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	p.hits.Store(0)
	p.status.Store(500)
	ctx := context.Background()
	e.clk.advance(time.Hour) // due now
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour}
	stored0, _ := e.st.Load()
	for i, delay := range want {
		before := p.hits.Load()
		e.svc.runDue(ctx)
		if cur, _ := e.st.Load(); e.st.saves != 0 || !reflect.DeepEqual(cur, stored0) {
			t.Fatalf("step %d: failing scheduled update touched the store", i)
		}
		if p.hits.Load() != before+1 {
			t.Fatalf("step %d: expected one request, got %d", i, p.hits.Load()-before)
		}
		e.clk.advance(delay - time.Second)
		e.svc.runDue(ctx)
		if p.hits.Load() != before+1 {
			t.Fatalf("step %d: retried before the %s delay", i, delay)
		}
		e.clk.advance(time.Second)
	}
	// success resets the backoff and stores a fresh LastGood
	p.status.Store(200)
	e.svc.runDue(ctx)
	subs, _ := e.st.Load()
	if !subs[0].LastGood.Equal(e.clk.Now()) {
		t.Fatal("success not stored")
	}
	if len(e.svc.sched) != 0 {
		t.Fatal("backoff not reset")
	}
}

func TestDeviceLimitKeepsProfiles(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub := e.seed(t, p.URL+"/s", 0)
	before, _ := e.st.Load()
	p.extra.Store(http.Header{"X-Hwid-Limit": {"true"}})
	p.body.Store("")
	_, err := e.svc.Update(context.Background(), sub.ID, User)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeDeviceLimit {
		t.Fatal(err)
	}
	after, _ := e.st.Load()
	if e.st.saves != 0 || len(after[0].Profiles) != 1 || !after[0].LastGood.Equal(before[0].LastGood) {
		t.Fatalf("stored subscription changed: saves=%d", e.st.saves)
	}
}

func TestInvalidProfileDropped(t *testing.T) {
	p := newProvider(t, nodeA+nodeB)
	e := newEnv(t, func(d *Deps) {
		d.BuildCheck = func(pr profile.Profile) error {
			if pr.Name == "beta" {
				return errors.New("secret detail uuid")
			}
			return nil
		}
	})
	sub, diff, err := e.svc.Add(context.Background(), p.URL+"/s", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Profiles) != 1 || sub.Profiles[0].Name != "alpha" || len(diff.Added) != 1 {
		t.Fatalf("%+v %+v", sub, diff)
	}
	if len(diff.Warnings) != 2 || diff.Warnings[0] != "profile_invalid: beta" || diff.Warnings[1] != "plain_http" {
		t.Fatalf("%v", diff.Warnings)
	}

	// none survive: error, nothing saved
	p.body.Store(nodeB)
	e.st.saves = 0
	_, err = e.svc.Update(context.Background(), sub.ID, User)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeInvalidProfiles || e.st.saves != 0 {
		t.Fatalf("%v saves=%d", err, e.st.saves)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("build error leaked")
	}
}

type capFetcher struct{ req subfetch.Request }

func (c *capFetcher) Fetch(_ context.Context, r subfetch.Request) (subfetch.Result, error) {
	c.req = r
	return subfetch.Result{}, errors.New("stop")
}

func TestPlainHTTPNoHWID(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub, diff, err := e.svc.Add(context.Background(), p.URL+"/s", "MyUA")
	if err != nil {
		t.Fatal(err)
	}
	h := p.headers.Load().(http.Header)
	if h.Get("X-Hwid") != "" || h.Get("X-Device-Os") != "" || h.Get("X-Ver-Os") != "" || h.Get("X-Device-Model") != "" {
		t.Fatalf("device headers over plain http: %v", h)
	}
	if h.Get("User-Agent") != "MyUA" {
		t.Fatalf("UA %q", h.Get("User-Agent"))
	}
	if len(diff.Warnings) != 1 || diff.Warnings[0] != "plain_http" {
		t.Fatalf("%v", diff.Warnings)
	}
	_ = sub

	// https: the headers are sent
	cf := &capFetcher{}
	e2 := newEnv(t, func(d *Deps) { d.Fetcher = cf })
	_, _, _ = e2.svc.Add(context.Background(), "https://prov.example.com/s", "")
	if cf.req.HWID.Get("X-Hwid") == "" || cf.req.HWID.Get("X-Device-Os") != "windows" {
		t.Fatalf("%v", cf.req.HWID)
	}
}

func TestThroughTunnelWhenConnected(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub := e.seed(t, p.URL+"/s", 0)
	var dials atomic.Int32
	e.tun.dial = func(ctx context.Context, n, a string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, n, a)
	}
	e.tun.up.Store(true)
	p.hits.Store(0)
	if _, err := e.svc.Update(context.Background(), sub.ID, User); err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 1 || p.hits.Load() != 1 {
		t.Fatalf("dials=%d hits=%d", dials.Load(), p.hits.Load())
	}
}

func TestNoURLInErrorsOrList(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	secretURL := p.URL + "/sub/SECRETTOKEN?key=SECRETQUERY"
	sub, _, err := e.svc.Add(context.Background(), secretURL, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sub.URL, "SECRET") || sub.URL != strings.TrimPrefix(p.URL, "http://") {
		t.Fatalf("add returned %q", sub.URL)
	}
	for _, s := range e.svc.List() {
		if strings.Contains(s.URL, "SECRET") || s.URL != strings.TrimPrefix(p.URL, "http://") {
			t.Fatalf("list %q", s.URL)
		}
	}
	stored, _ := e.st.Load()
	if !strings.Contains(stored[0].URL, "SECRETTOKEN") {
		t.Fatal("store lost the URL")
	}

	check := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("want error")
		}
		for _, bad := range []string{"SECRET", p.URL, "127.0.0.1", "machine-secret-id", hwid.ID("machine-secret-id", strings.TrimPrefix(p.URL, "http://"))} {
			if strings.Contains(err.Error(), bad) {
				t.Fatalf("error leaks %q: %v", bad, err)
			}
		}
	}
	p.status.Store(500)
	_, err = e.svc.Update(context.Background(), stored[0].ID, User)
	check(err)
	p.Close()
	_, err = e.svc.Update(context.Background(), stored[0].ID, User)
	check(err)
	_, _, err = e.svc.Add(context.Background(), secretURL, "")
	check(err)
	_, _, err = e.svc.Add(context.Background(), "ftp://SECRET@host/x", "")
	check(err)
}

func TestStoreSavedOnlyOnSuccess(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	p.status.Store(500)
	if _, _, err := e.svc.Add(context.Background(), p.URL+"/s", ""); err == nil {
		t.Fatal("want error")
	}
	if e.st.saves != 0 {
		t.Fatal("saved on failure")
	}
	p.status.Store(200)
	p.body.Store("garbage that is no subscription at all <html>")
	if _, _, err := e.svc.Add(context.Background(), p.URL+"/s", ""); err == nil {
		t.Fatal("want error")
	}
	if e.st.saves != 0 {
		t.Fatal("saved on failure")
	}
	p.body.Store(nodeA)
	sub, _, err := e.svc.Add(context.Background(), p.URL+"/s", "")
	if err != nil || e.st.saves != 1 {
		t.Fatal(err, e.st.saves)
	}
	// a failing update saves nothing
	p.status.Store(503)
	if _, err := e.svc.Update(context.Background(), sub.ID, User); err == nil || e.st.saves != 1 {
		t.Fatal(err, e.st.saves)
	}
}

func TestConcurrentUpdateOneRequest(t *testing.T) {
	var hold atomic.Bool
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if hold.Load() {
			<-release
		}
		_, _ = w.Write([]byte(nodeA))
	}))
	defer srv.Close()
	e := newEnv(t, nil)
	sub := e.seed(t, srv.URL+"/s", 0)
	hits.Store(0)
	hold.Store(true)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.svc.Update(context.Background(), sub.ID, User)
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
}

func TestRemoveSetUAEvents(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub := e.seed(t, p.URL+"/s", 0)
	if err := e.svc.SetUA(sub.ID, "X/1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.Load(); got[0].UA != "X/1" {
		t.Fatal("ua")
	}
	_, _ = e.svc.Update(context.Background(), sub.ID, User) // Updated
	p.status.Store(500)
	_, _ = e.svc.Update(context.Background(), sub.ID, User) // Failed: the latest
	select {
	case ev := <-e.svc.Events():
		if ev.Kind != Failed || ev.ID != sub.ID || ev.Code != CodeHTTPStatus {
			t.Fatalf("%+v", ev)
		}
	default:
		t.Fatal("no event")
	}
	if err := e.svc.Remove(sub.ID); err != nil {
		t.Fatal(err)
	}
	var se *Error
	if err := e.svc.Remove(sub.ID); !errors.As(err, &se) || se.Code != CodeNotFound {
		t.Fatal(err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestScheduledDeviceLimitKeepsProfiles(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	e.seed(t, p.URL+"/s", time.Hour)
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	before, _ := e.st.Load()
	p.hits.Store(0)
	p.extra.Store(http.Header{"X-Hwid-Limit": {"true"}})
	p.body.Store("")
	e.clk.advance(2 * time.Hour)
	e.svc.runDue(context.Background())
	e.svc.runDue(context.Background()) // within the backoff: no burst
	after, _ := e.st.Load()
	if p.hits.Load() != 1 || e.st.saves != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("hits=%d saves=%d", p.hits.Load(), e.st.saves)
	}
}

func TestExistingProfileKeepsOldVersionWhenInvalid(t *testing.T) {
	p := newProvider(t, nodeA+nodeB)
	var bad atomic.Bool
	e := newEnv(t, func(d *Deps) {
		d.BuildCheck = func(pr profile.Profile) error {
			if bad.Load() && pr.Name == "beta2" {
				return errors.New("broken")
			}
			return nil
		}
	})
	sub, _, err := e.svc.Add(context.Background(), p.URL+"/s", "")
	if err != nil || len(sub.Profiles) != 2 {
		t.Fatal(err)
	}
	bad.Store(true)
	p.body.Store(nodeA + strings.Replace(nodeB, "#beta", "#beta2", 1))
	diff, err := e.svc.Update(context.Background(), sub.ID, User)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := e.st.Load()
	if len(stored[0].Profiles) != 2 || stored[0].Profiles[1].Name != "beta" {
		t.Fatalf("old version lost: %+v", stored[0].Profiles)
	}
	if len(diff.Removed) != 0 || len(diff.Changed) != 0 || len(diff.Added) != 0 {
		t.Fatalf("%+v", diff)
	}
	if len(diff.Warnings) != 2 || diff.Warnings[0] != "profile_kept_old: beta2" {
		t.Fatalf("%v", diff.Warnings)
	}
	// only a changed-and-broken old profile left: kept-old counts as valid
	p.body.Store(strings.Replace(nodeB, "#beta", "#beta2", 1))
	if _, err := e.svc.Update(context.Background(), sub.ID, User); err != nil {
		t.Fatal(err)
	}
}

func TestSetUADuringFetchSurvives(t *testing.T) {
	release := make(chan struct{})
	var hold atomic.Bool
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if hold.Load() {
			<-release
		}
		_, _ = w.Write([]byte(nodeA))
	}))
	defer srv.Close()
	e := newEnv(t, nil)
	sub := e.seed(t, srv.URL+"/s", 0)
	hold.Store(true)
	done := make(chan error, 1)
	go func() { _, err := e.svc.Update(context.Background(), sub.ID, User); done <- err }()
	for hits.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if err := e.svc.SetUA(sub.ID, "Mid/1"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.Load()
	if got[0].UA != "Mid/1" {
		t.Fatalf("UA lost: %q", got[0].UA)
	}
}

type blockFetcher struct {
	calls   atomic.Int32
	release chan struct{}
	inner   Fetcher
	hold    atomic.Bool
}

func (b *blockFetcher) Fetch(ctx context.Context, r subfetch.Request) (subfetch.Result, error) {
	b.calls.Add(1)
	if b.hold.Load() {
		<-b.release
	}
	return b.inner.Fetch(ctx, r)
}

func TestButtonNeverJoinsScheduledFlight(t *testing.T) {
	p := newProvider(t, nodeA)
	bf := &blockFetcher{release: make(chan struct{}), inner: subfetch.New(5 * time.Second)}
	e := newEnv(t, func(d *Deps) { d.Fetcher = bf })
	sub := e.seed(t, p.URL+"/s", 0)
	bf.calls.Store(0)
	bf.hold.Store(true)
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = e.svc.Update(context.Background(), sub.ID, Scheduled) }()
	for bf.calls.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	go func() { defer wg.Done(); _, _ = e.svc.Update(context.Background(), sub.ID, User) }()
	for bf.calls.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	close(bf.release)
	wg.Wait()
}

func TestCancelOneCallerDoesNotFailOthers(t *testing.T) {
	var hold atomic.Bool
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if hold.Load() {
			<-release
		}
		_, _ = w.Write([]byte(nodeA))
	}))
	defer srv.Close()
	e := newEnv(t, nil)
	sub := e.seed(t, srv.URL+"/s", 0)
	hits.Store(0)
	hold.Store(true)
	c1, cancel1 := context.WithCancel(context.Background())
	res1 := make(chan error, 1)
	go func() { _, err := e.svc.Update(c1, sub.ID, User); res1 <- err }()
	for hits.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	res2 := make(chan error, 1)
	go func() { _, err := e.svc.Update(context.Background(), sub.ID, User); res2 <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel1()
	if err := <-res1; err == nil {
		t.Fatal("canceled caller should return an error")
	}
	close(release)
	if err := <-res2; err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
}

func TestStartTrigger(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub := e.seed(t, p.URL+"/s", time.Hour)
	e.clk.advance(5 * time.Hour) // due
	p.hits.Store(0)
	p.status.Store(500)
	// Start without a tunnel: one direct request, no retry
	if _, err := e.svc.Update(context.Background(), sub.ID, Start); err == nil {
		t.Fatal("want failure")
	}
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done
	if p.hits.Load() != 1 {
		t.Fatalf("Start then Run made %d requests, want 1", p.hits.Load())
	}
	// Start through the tunnel
	var dials atomic.Int32
	e.tun.dial = func(ctx context.Context, n, a string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, n, a)
	}
	p.status.Store(200)
	if _, err := e.svc.Update(context.Background(), sub.ID, Start); err != nil || dials.Load() != 1 {
		t.Fatal(err, dials.Load())
	}
}

func TestIntervalDefaultsAndMinimum(t *testing.T) {
	if interval(0) != 24*time.Hour || interval(time.Minute) != time.Hour || interval(6*time.Hour) != 6*time.Hour {
		t.Fatal("interval rules")
	}
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	e.seed(t, p.URL+"/s", 0) // default 24 h
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	p.hits.Store(0)
	e.clk.advance(23 * time.Hour)
	e.svc.runDue(context.Background())
	if p.hits.Load() != 0 {
		t.Fatal("ran before 24 h")
	}
	e.clk.advance(time.Hour)
	e.svc.runDue(context.Background())
	if p.hits.Load() != 1 {
		t.Fatal("did not run at 24 h")
	}
	// a 1 minute provider interval is raised to 1 h
	subs, _ := e.st.Load()
	subs[0].Info.UpdateInterval = time.Minute
	_ = e.st.Save(subs)
	p.hits.Store(0)
	e.clk.advance(30 * time.Minute)
	e.svc.runDue(context.Background())
	if p.hits.Load() != 0 {
		t.Fatal("interval below 1 h honored")
	}
	e.clk.advance(30 * time.Minute)
	e.svc.runDue(context.Background())
	if p.hits.Load() != 1 {
		t.Fatal("did not run at 1 h")
	}
}

// FINAL #1: a failed Start refresh backs off like a failed scheduled update;
// it does not hold the scheduler for the whole interval.
func TestFailedStartBacksOff(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub := e.seed(t, p.URL+"/s", 24*time.Hour)
	p.hits.Store(0)
	p.status.Store(500)
	ctx := context.Background()
	if _, err := e.svc.Update(ctx, sub.ID, Start); err == nil {
		t.Fatal("want failure")
	}
	if p.hits.Load() != 1 {
		t.Fatalf("Start made %d requests", p.hits.Load())
	}
	// without the tunnel nothing is sent, however long we wait
	e.clk.advance(2 * time.Minute)
	e.svc.runDue(ctx)
	if p.hits.Load() != 1 {
		t.Fatalf("sent %d requests without a tunnel", p.hits.Load())
	}
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	e.svc.runDue(ctx)
	if p.hits.Load() != 2 {
		t.Fatalf("after the first backoff with the tunnel up: %d requests, want 2", p.hits.Load())
	}
	e.svc.runDue(ctx)
	if p.hits.Load() != 2 {
		t.Fatal("retried at once")
	}
	for i, delay := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute} {
		before := p.hits.Load()
		e.clk.advance(delay - time.Second)
		e.svc.runDue(ctx)
		if p.hits.Load() != before {
			t.Fatalf("step %d: retried before %s", i, delay)
		}
		e.clk.advance(time.Second)
		e.svc.runDue(ctx)
		if p.hits.Load() != before+1 {
			t.Fatalf("step %d: %d requests after %s, want 1", i, p.hits.Load()-before, delay)
		}
	}
}

// The scheduler right after a failed Start, still inside the first backoff,
// sends nothing.
func TestFailedStartNoImmediateScheduled(t *testing.T) {
	p := newProvider(t, nodeA)
	e := newEnv(t, nil)
	sub := e.seed(t, p.URL+"/s", time.Hour)
	e.clk.advance(5 * time.Hour) // due by LastGood
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	p.hits.Store(0)
	p.status.Store(500)
	if _, err := e.svc.Update(context.Background(), sub.ID, Start); err == nil {
		t.Fatal("want failure")
	}
	e.svc.runDue(context.Background())
	if p.hits.Load() != 1 {
		t.Fatalf("%d requests, want 1", p.hits.Load())
	}
}

// Item 8: a canceled scheduled update is not a failure: no backoff growth,
// no failure log, and the code is "canceled".
func TestCanceledScheduledUpdateNoBackoff(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) > 1 {
			<-release
		}
		_, _ = w.Write([]byte(nodeA))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	var logs atomic.Int32
	e := newEnv(t, func(d *Deps) { d.Logf = func(string, ...any) { logs.Add(1) } })
	sub := e.seed(t, srv.URL+"/s", time.Hour)
	e.tun.up.Store(true)
	e.tun.dial = (&net.Dialer{}).DialContext
	e.clk.advance(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.runDue(ctx); close(done) }()
	for hits.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if logs.Load() != 0 {
		t.Fatalf("canceled update logged %d lines", logs.Load())
	}
	e.svc.mu.Lock()
	b := e.svc.sched[sub.ID]
	e.svc.mu.Unlock()
	if b != nil {
		t.Fatalf("canceled update changed the backoff: %+v", *b)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	_, err := e.svc.Update(cctx, sub.ID, User)
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeCanceled {
		t.Fatalf("canceled Update = %v, want code %s", err, CodeCanceled)
	}
}
