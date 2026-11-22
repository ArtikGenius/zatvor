package xrayrun

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain turns the test binary into a fake "xray": when it is started as
// `<bin> run -config stdin:` it reads the behaviour from the file "mode" in
// XRAY_LOCATION_ASSET instead of running tests.
func TestMain(m *testing.M) {
	if len(os.Args) >= 4 && os.Args[1] == "run" && os.Args[2] == "-config" && os.Args[3] == "stdin:" {
		os.Exit(fakeXray())
	}
	os.Exit(m.Run())
}

func fakeXray() int {
	dir := os.Getenv("XRAY_LOCATION_ASSET")
	modeB, _ := os.ReadFile(filepath.Join(dir, "mode"))
	mode := strings.TrimSpace(string(modeB))
	in, _ := io.ReadAll(os.Stdin)
	switch mode {
	case "echo":
		_ = os.WriteFile(filepath.Join(dir, "stdin.out"), in, 0o600)
		select {} // wait to be stopped
	case "crash":
		os.Stderr.WriteString("boom " + strings.TrimSpace(string(in)) + "\n")
		return 1
	case "stubborn":
		ignoreInterrupt()
		select {}
	default: // "sleep"
		select {}
	}
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{c.now.Add(d), ch})
	return ch
}

// Advance moves time forward by d and fires the timers that are due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

func newRunner(t *testing.T, mode string, clk Clock) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	return New(os.Args[0], dir, clk), dir
}

func next(t *testing.T, r *Runner, kind EventKind) Event {
	t.Helper()
	for {
		select {
		case ev := <-r.Events():
			if ev.Kind == kind {
				return ev
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("timeout waiting for event kind %v", kind)
		}
	}
}

func TestConfigViaStdin(t *testing.T) {
	r, dir := newRunner(t, "echo", RealClock{})
	cfg := []byte(`{"outbounds":[{"protocol":"freedom"}]}`)
	if err := r.Start(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	defer r.Stop(5 * time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := os.ReadFile(filepath.Join(dir, "stdin.out"))
		if err == nil && len(got) > 0 {
			if !bytes.Equal(got, cfg) {
				t.Fatalf("stdin = %q, want %q", got, cfg)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake xray never wrote stdin")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := r.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "mode,stdin.out" {
		t.Errorf("unexpected files in work dir: %v", names)
	}
}

func TestBackoff(t *testing.T) {
	clk := newFakeClock()
	r, _ := newRunner(t, "crash", clk)
	if err := r.Start(context.Background(), []byte(`{"password":"SECRET_REPLACE_ME"}`), nil); err != nil {
		t.Fatal(err)
	}
	defer r.Stop(time.Second)
	// Crashes at t=0,1,3,7,15 (fake seconds).
	for i, want := range []time.Duration{1, 2, 4, 8} {
		ex := next(t, r, Exited)
		if ex.Err == nil || !strings.Contains(ex.Err.Error(), "boom") {
			t.Fatalf("crash %d: Exited.Err = %v", i, ex.Err)
		}
		if strings.Contains(ex.Err.Error(), "SECRET_REPLACE_ME") {
			t.Fatalf("secret leaked in %v", ex.Err)
		}
		rs := next(t, r, Restarting)
		if rs.Delay != want*time.Second {
			t.Fatalf("crash %d: delay %v, want %v", i, rs.Delay, want*time.Second)
		}
		clk.Advance(rs.Delay)
	}
	next(t, r, Exited)
	if gu := next(t, r, GaveUp); gu.Err == nil {
		t.Error("GaveUp without error")
	}
	select {
	case ev := <-r.Events():
		t.Errorf("event after GaveUp: %v", ev.Kind)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBackoffWindowSlides(t *testing.T) {
	clk := newFakeClock()
	r, _ := newRunner(t, "crash", clk)
	if err := r.Start(context.Background(), []byte("x"), nil); err != nil {
		t.Fatal(err)
	}
	defer r.Stop(time.Second)
	// Crashes spaced 30 s apart never put 5 inside a 2-minute window.
	for i := 0; i < 8; i++ {
		next(t, r, Exited)
		rs := next(t, r, Restarting)
		clk.Advance(30 * time.Second)
		_ = rs
	}
}

func TestStopKillsOnlyOurChild(t *testing.T) {
	r, _ := newRunner(t, "sleep", RealClock{})
	// A decoy with the same binary name must survive.
	decoy := exec.Command(os.Args[0], "run", "-config", "stdin:")
	decoy.Env = []string{"XRAY_LOCATION_ASSET=" + t.TempDir()}
	decoy.Stdin = strings.NewReader("")
	if err := decoy.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = decoy.Process.Kill(); _ = decoy.Wait() }()

	if err := r.Start(context.Background(), []byte("{}"), nil); err != nil {
		t.Fatal(err)
	}
	pid := r.Pid()
	if pid == 0 || pid == decoy.Process.Pid {
		t.Fatalf("bad pid %d", pid)
	}
	if err := r.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if r.Pid() != 0 {
		t.Error("Pid not cleared after Stop")
	}
	if alive(pid) {
		t.Error("our child is still alive")
	}
	if !alive(decoy.Process.Pid) {
		t.Error("decoy process was killed")
	}
}

func TestStopKillsStubbornChild(t *testing.T) {
	r, _ := newRunner(t, "stubborn", RealClock{})
	if err := r.Start(context.Background(), []byte("{}"), nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let the child install its handler
	pid := r.Pid()
	if err := r.Stop(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if alive(pid) {
		t.Error("stubborn child survived Stop")
	}
}

func TestStartReadyFailureStops(t *testing.T) {
	r, _ := newRunner(t, "sleep", RealClock{})
	ready := func(ctx context.Context) error { return context.DeadlineExceeded }
	if err := r.Start(context.Background(), []byte("{}"), ready); err == nil {
		t.Fatal("expected error")
	}
	if r.Pid() != 0 {
		t.Error("child still running after failed readiness")
	}
}

func TestStartChildExitsBeforeReady(t *testing.T) {
	r, _ := newRunner(t, "crash", newFakeClock())
	err := r.Start(context.Background(), []byte(`{"password":"SECRET_REPLACE_ME"}`), func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "SECRET_REPLACE_ME") {
		t.Fatalf("err = %v", err)
	}
}

func TestDialReady(t *testing.T) {
	ln := listenLoopback(t)
	defer ln.Close()
	if err := DialReady(ln.Addr().String())(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	addr := ln.Addr().String()
	ln.Close()
	if err := DialReady(addr)(ctx); err == nil {
		t.Fatal("expected error for closed port")
	}
}

func TestRingBufferCap(t *testing.T) {
	b := newRingBuf(8)
	b.Write([]byte("0123456789"))
	b.Write([]byte("ab"))
	if got := b.String(); got != "456789ab" {
		t.Errorf("got %q", got)
	}
}

func TestStartSpawnFailureReleasesStop(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "no-such-xray"), t.TempDir(), RealClock{})
	stopped := make(chan struct{})
	quit := make(chan struct{})
	go func() { // hammer Stop while Start fails
		defer close(stopped)
		for {
			select {
			case <-quit:
				return
			default:
				_ = r.Stop(time.Second)
			}
		}
	}()
	for i := 0; i < 200; i++ {
		if err := r.Start(context.Background(), []byte("{}"), nil); err == nil {
			t.Fatal("expected spawn error")
		}
	}
	close(quit)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Stop blocked forever after spawn failure")
	}
}
