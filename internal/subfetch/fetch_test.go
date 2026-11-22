package subfetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

)

const linkBody = "vless://00000000-0000-4000-8000-000000000000@example.com:443?security=tls#n1\n"

func TestOneRequest(t *testing.T) {
	var n atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		<-release
		_, _ = w.Write([]byte(linkBody))
	}))
	defer srv.Close()
	f := New(5 * time.Second)
	var wg sync.WaitGroup
	results := make([]Result, 10)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.Fetch(context.Background(), Request{URL: srv.URL + "/sub", UA: "ua"})
			if err != nil {
				t.Error(err)
			}
			results[i] = r
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := n.Load(); got != 1 {
		t.Fatalf("server saw %d requests, want 1", got)
	}
	for i, r := range results {
		if r.Problem != None || len(r.Body.Nodes) != 1 {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	// Callers get independent copies.
	results[0].Body.Nodes[0].Remark = "changed"
	if results[1].Body.Nodes[0].Remark == "changed" {
		t.Fatal("results share state")
	}
}

func TestHeadersSent(t *testing.T) {
	var got http.Header
	var gz bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if gz {
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = zw.Write([]byte(linkBody))
			_ = zw.Close()
			return
		}
		_, _ = w.Write([]byte(linkBody))
	}))
	defer srv.Close()
	f := New(5 * time.Second)

	res, err := f.Fetch(context.Background(), Request{URL: srv.URL + "/a", UA: DefaultUA("1.0.0", "windows")})
	if err != nil || res.Problem != None {
		t.Fatal(err, res.Problem)
	}
	if got.Get("User-Agent") != "Zatvor/1.0.0 (windows)" || got.Get("Accept-Encoding") != "gzip" {
		t.Fatalf("headers: %v", got)
	}
	if got.Get("X-Hwid") != "" {
		t.Fatal("HWID header sent without being given")
	}

	gz = true
	hw := http.Header{}
	hw.Set("x-hwid", "abc")
	hw.Set("x-device-os", "Windows")
	res, err = f.Fetch(context.Background(), Request{URL: srv.URL + "/b", UA: "ua", HWID: hw})
	if err != nil || res.Problem != None || len(res.Body.Nodes) != 1 {
		t.Fatalf("gzip: %v %+v", err, res)
	}
	if got.Get("X-Hwid") != "abc" || got.Get("X-Device-Os") != "Windows" {
		t.Fatalf("HWID headers: %v", got)
	}
}

func TestTimeout(t *testing.T) {
	stall := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(stall)
	f := New(100 * time.Millisecond)
	res, err := f.Fetch(context.Background(), Request{URL: srv.URL + "/sub/SECRETTOKEN", UA: "ua"})
	if err == nil || res.Problem != Network {
		t.Fatalf("err=%v problem=%v", err, res.Problem)
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatal("secret in error")
	}
}

func TestProblems(t *testing.T) {
	cases := []struct {
		name   string
		status int
		hdr    map[string]string
		body   string
		want   Problem
	}{
		{"limit", 404, map[string]string{"x-hwid-max-devices-reached": "true"}, "", DeviceLimit},
		{"hwid required", 200, map[string]string{"x-hwid-not-supported": "true"}, "", HWIDRequired},
		{"html", 200, nil, "<html><body>hi</body></html>", NotASubscription},
		{"empty", 200, nil, "", EmptyBody},
		{"500", 500, nil, "oops", HTTPStatus},
		{"clash", 200, nil, "proxies:\n  - name: a\n    type: ss\n", ClashUnsupported},
		{"ok", 200, nil, linkBody, None},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.hdr {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			res, err := New(5*time.Second).Fetch(context.Background(), Request{URL: srv.URL, UA: "ua"})
			if err != nil {
				t.Fatal(err)
			}
			if res.Problem != c.want || res.Status != c.status {
				t.Fatalf("problem=%v status=%d, want %v %d", res.Problem, res.Status, c.want, c.status)
			}
		})
	}
}

func TestBodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxBody+10))
	}))
	defer srv.Close()
	res, err := New(10*time.Second).Fetch(context.Background(), Request{URL: srv.URL, UA: "ua"})
	if err != nil || res.Problem != NotASubscription {
		t.Fatalf("err=%v problem=%v", err, res.Problem)
	}
}

func TestNoSecretInError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/sub/SECRETTOKEN?k=SECRETQUERY"
	srv.Close() // connection refused
	res, err := New(2*time.Second).Fetch(context.Background(), Request{URL: url, UA: "ua"})
	if err == nil || res.Problem != Network {
		t.Fatalf("err=%v problem=%v", err, res.Problem)
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), "SECRETQUERY") {
		t.Fatalf("secret in error: %v", err)
	}
	if _, err := New(time.Second).Fetch(context.Background(), Request{URL: "://bad/SECRETTOKEN", UA: "ua"}); err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("bad url error: %v", err)
	}
}

func TestCrossHostRedirectNotFollowed(t *testing.T) {
	var other atomic.Int32
	o := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { other.Add(1) }))
	defer o.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(o.URL, "127.0.0.1", "localhost", 1), http.StatusFound)
	}))
	defer srv.Close()
	res, err := New(2*time.Second).Fetch(context.Background(), Request{URL: srv.URL, UA: "ua"})
	if err != nil || res.Problem != HTTPStatus || other.Load() != 0 {
		t.Fatalf("err=%v problem=%v other=%d", err, res.Problem, other.Load())
	}
}

func TestUAHelpers(t *testing.T) {
	if DefaultUA("1.0.0", "windows") != "Zatvor/1.0.0 (windows)" || DefaultUA("1.0.0", "macos") != "Zatvor/1.0.0 (macos)" {
		t.Fatal("DefaultUA")
	}
	if HappCompatibleUA("1.0.0", "windows") != "Zatvor/1.0.0 (windows; happ-compatible)" ||
		HappCompatibleUA("1.0.0", "macos") != "Zatvor/1.0.0 (macos; happ-compatible)" {
		t.Fatal("HappCompatibleUA")
	}
	if HappUA("1.0.0", "windows") != "Happ/1.0.0/Windows/zatvor" || HappUA("1.0.0", "macos") != "Happ/1.0.0/macos/zatvor" {
		t.Fatal("HappUA")
	}
}

