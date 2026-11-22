package supervisor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/journal"
	"github.com/ArtikGenius/zatvor/internal/supervisor"
)

// ---- the fake child: this test binary, re-run on TestHelperChild only ----

const (
	helperEnv  = "ZATVOR_SUPERVISOR_HELPER"
	counterEnv = "ZATVOR_SUPERVISOR_COUNTER"
	crashesEnv = "ZATVOR_SUPERVISOR_CRASHES"
	thenEnv    = "ZATVOR_SUPERVISOR_THEN"
)

// TestHelperChild is the fake daemon. Outside a supervisor test it does
// nothing. It counts its starts in a file, exits 3 for its first CRASHES
// starts, then does THEN: "run" (until stdin closes, then exit 0), "exit0",
// or "ignore" (run until killed, never reading stdin).
func TestHelperChild(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	path := os.Getenv(counterEnv)
	n := 0
	if b, err := os.ReadFile(path); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	if err := os.WriteFile(path+".tmp", []byte(strconv.Itoa(n)), 0o600); err != nil {
		os.Exit(9)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		os.Exit(9)
	}
	crashes, _ := strconv.Atoi(os.Getenv(crashesEnv))
	if crashes < 0 || n <= crashes {
		os.Exit(3)
	}
	switch os.Getenv(thenEnv) {
	case "run":
		_, _ = io.Copy(io.Discard, bufio.NewReader(os.Stdin))
		os.Exit(0)
	case "ignore":
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	default:
		os.Exit(0)
	}
}

type child struct {
	t       *testing.T
	counter string
	mu      sync.Mutex
	cmds    []*exec.Cmd
}

func newChild(t *testing.T) *child {
	return &child{t: t, counter: filepath.Join(t.TempDir(), "starts")}
}

// factory starts the helper, which exits 3 for its first crashes starts
// (-1: always), then does then.
func (c *child) factory(crashes int, then string) func() *exec.Cmd {
	return func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperChild$")
		cmd.Env = append(os.Environ(), helperEnv+"=1", counterEnv+"="+c.counter,
			crashesEnv+"="+strconv.Itoa(crashes), thenEnv+"="+then)
		c.mu.Lock()
		c.cmds = append(c.cmds, cmd)
		c.mu.Unlock()
		return cmd
	}
}

func (c *child) starts() int {
	b, err := os.ReadFile(c.counter)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// lastExited reports whether the newest child's Wait has returned.
func (c *child) lastExited() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cmds) == 0 {
		return true
	}
	return c.cmds[len(c.cmds)-1].ProcessState != nil
}

// ---- fake clock: backoff waits fire at once, and are recorded ----

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	delays []time.Duration
}

func newClock() *fakeClock { return &fakeClock{now: time.Unix(3_000_000, 0)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delays = append(c.delays, d)
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func (c *fakeClock) Delays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.delays...)
}

// ---- journal with one pending sysproxy-like entry over a fake reverter ----

type fakeReverter struct {
	mu       sync.Mutex
	state    map[string]json.RawMessage
	restores int
	onCall   func()
}

func (r *fakeReverter) Current(target string) (json.RawMessage, error) {
	if r.onCall != nil {
		r.onCall()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state[target], nil
}

func (r *fakeReverter) Restore(target string, prev json.RawMessage) error {
	if r.onCall != nil {
		r.onCall()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[target] = prev
	r.restores++
	return nil
}

func (r *fakeReverter) Restores() int { r.mu.Lock(); defer r.mu.Unlock(); return r.restores }

func pendingJournal(t *testing.T) (*journal.Journal, *fakeReverter) {
	t.Helper()
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ours := json.RawMessage(`{"enabled":true,"server":"127.0.0.1:31001"}`)
	if _, err := j.Begin(journal.Entry{Kind: "fake", Target: "lan", Prev: json.RawMessage(`{"enabled":false}`), Applied: ours}); err != nil {
		t.Fatal(err)
	}
	return j, &fakeReverter{state: map[string]json.RawMessage{"lan": ours}}
}

func pendingCount(t *testing.T, j *journal.Journal) int {
	t.Helper()
	p, err := j.Pending()
	if err != nil {
		t.Fatal(err)
	}
	return len(p)
}

type result struct{ err error }

func run(ctx context.Context, f func() *exec.Cmd, j *journal.Journal, r *fakeReverter, clk *fakeClock, opts ...supervisor.Option) <-chan result {
	out := make(chan result, 1)
	go func() {
		out <- result{supervisor.Run(ctx, f, j, map[string]journal.Reverter{"fake": r}, clk, opts...)}
	}()
	return out
}

func wait(t *testing.T, ch <-chan result) error {
	t.Helper()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(60 * time.Second):
		t.Fatal("supervisor.Run did not return")
		return nil
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func seconds(ds []time.Duration) string { return fmt.Sprint(ds) }

// ---- tests ----

func TestSupervisorRestarts(t *testing.T) {
	c := newChild(t)
	j, r := pendingJournal(t)
	clk := newClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := run(ctx, c.factory(3, "run"), j, r, clk)

	waitFor(t, "the fourth start", func() bool { return c.starts() == 4 })
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if got := clk.Delays(); seconds(got) != seconds(want) {
		t.Fatalf("backoff delays = %v, want %v", got, want)
	}
	cancel()
	if err := wait(t, ch); err != nil {
		t.Fatalf("Run after a clean stop = %v, want nil", err)
	}
	if r.Restores() != 0 || pendingCount(t, j) != 1 {
		t.Fatalf("a cleanly stopped child must not be reverted for (restores %d, pending %d)", r.Restores(), pendingCount(t, j))
	}
	if !c.lastExited() {
		t.Fatal("Run returned while the child was alive")
	}
}

func TestSupervisorGivesUpAndReverts(t *testing.T) {
	c := newChild(t)
	j, r := pendingJournal(t)
	// R10: the journal is never used while a child is alive.
	r.onCall = func() {
		if !c.lastExited() {
			t.Error("reverter called while the child is alive")
		}
	}
	clk := newClock()
	err := wait(t, run(context.Background(), c.factory(-1, ""), j, r, clk))

	if !errors.Is(err, supervisor.ErrGaveUp) {
		t.Fatalf("Run = %v, want ErrGaveUp", err)
	}
	var gu *supervisor.GaveUpError
	if !errors.As(err, &gu) {
		t.Fatalf("Run = %T, want *GaveUpError", err)
	}
	if gu.Crashes != 5 || gu.Restored != 1 || gu.Failed != 0 {
		t.Fatalf("GaveUpError = %+v, want 5 crashes and 1 restored", *gu)
	}
	if c.starts() != 5 {
		t.Fatalf("starts = %d, want 5", c.starts())
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if got := clk.Delays(); seconds(got) != seconds(want) {
		t.Fatalf("backoff delays = %v, want %v", got, want)
	}
	if r.Restores() != 1 || pendingCount(t, j) != 0 {
		t.Fatalf("restores %d, pending %d; want the entry reverted", r.Restores(), pendingCount(t, j))
	}
	if string(r.state["lan"]) != `{"enabled":false}` {
		t.Fatalf("lan = %s, want the previous value", r.state["lan"])
	}
}

func TestSupervisorDoesNotRevertOnCleanExit(t *testing.T) {
	c := newChild(t)
	j, r := pendingJournal(t)
	err := wait(t, run(context.Background(), c.factory(0, "exit0"), j, r, newClock()))
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if c.starts() != 1 {
		t.Fatalf("starts = %d, want 1 (a clean exit is not restarted)", c.starts())
	}
	if r.Restores() != 0 || pendingCount(t, j) != 1 {
		t.Fatal("a clean exit must not revert")
	}
}

func TestSupervisorWindowSlides(t *testing.T) {
	// Each child runs 3 minutes before it crashes: 5 crashes never fall
	// within 2 minutes, and a run longer than the window starts the backoff
	// again at 1 s.
	clk := newClock()
	defer supervisor.SetAfterStart(func() { clk.Advance(3 * time.Minute) })()
	c := newChild(t)
	j, r := pendingJournal(t)
	err := wait(t, run(context.Background(), c.factory(8, "exit0"), j, r, clk))
	if err != nil {
		t.Fatalf("Run = %v, want nil after 8 spaced crashes and a clean exit", err)
	}
	if c.starts() != 9 {
		t.Fatalf("starts = %d, want 9", c.starts())
	}
	for i, d := range clk.Delays() {
		if d != time.Second {
			t.Fatalf("delay %d = %v, want 1s for crashes after a long run", i, d)
		}
	}
	if r.Restores() != 0 {
		t.Fatal("no give-up, so no revert")
	}
}

func TestSupervisorBackoffCapsAt16s(t *testing.T) {
	// Each child runs 30 s: crashes at 30, 61, 93, 127, 165, 211, 257 s.
	// The window never holds 5, and the delays grow to 16 s and stay.
	clk := newClock()
	defer supervisor.SetAfterStart(func() { clk.Advance(30 * time.Second) })()
	c := newChild(t)
	j, r := pendingJournal(t)
	if err := wait(t, run(context.Background(), c.factory(7, "exit0"), j, r, clk)); err != nil {
		t.Fatalf("Run = %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 16 * time.Second, 16 * time.Second}
	if got := clk.Delays(); seconds(got) != seconds(want) {
		t.Fatalf("delays = %v, want %v", got, want)
	}
}

func TestSupervisorStopsChildOnCancel(t *testing.T) {
	c := newChild(t)
	j, r := pendingJournal(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := run(ctx, c.factory(0, "run"), j, r, newClock())
	waitFor(t, "the child", func() bool { return c.starts() == 1 })
	cancel()
	if err := wait(t, ch); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !c.lastExited() {
		t.Fatal("Run returned before the child exited")
	}
	if r.Restores() != 0 {
		t.Fatal("a child that exited cleanly reverts on its own")
	}
}

func TestSupervisorKillsChildThatIgnoresStopAndReverts(t *testing.T) {
	restore := supervisor.SetStopTimeout(200 * time.Millisecond)
	defer restore()
	c := newChild(t)
	j, r := pendingJournal(t)
	r.onCall = func() {
		if !c.lastExited() {
			t.Error("reverter called while the child is alive")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := run(ctx, c.factory(0, "ignore"), j, r, newClock())
	waitFor(t, "the child", func() bool { return c.starts() == 1 })
	cancel()
	if err := wait(t, ch); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if r.Restores() != 1 {
		t.Fatal("a killed child could not revert, so the supervisor must")
	}
}

func TestSupervisorRevertLockHeldSkipsRevert(t *testing.T) {
	c := newChild(t)
	j, r := pendingJournal(t)
	err := wait(t, run(context.Background(), c.factory(-1, ""), j, r, newClock(),
		supervisor.WithRevertLock(func() (func(), bool) { return func() {}, false })))
	var gu *supervisor.GaveUpError
	if !errors.As(err, &gu) || !gu.Locked {
		t.Fatalf("Run = %v, want a GaveUpError with Locked", err)
	}
	if r.Restores() != 0 || pendingCount(t, j) != 1 {
		t.Fatal("another instance owns the journal: nothing may be reverted")
	}
}

func TestSupervisorCancelledBeforeStartTouchesNothing(t *testing.T) {
	c := newChild(t)
	j, r := pendingJournal(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(t, run(ctx, c.factory(0, "run"), j, r, newClock())); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if r.Restores() != 0 || pendingCount(t, j) != 1 {
		t.Fatal("no child crashed, so nothing may be reverted")
	}
}

// blockingClock never fires a backoff wait; it reports each wait on waits.
type blockingClock struct {
	*fakeClock
	waits chan time.Duration
}

func (c *blockingClock) After(d time.Duration) <-chan time.Time {
	c.waits <- d
	return make(chan time.Time)
}

func TestSupervisorRevertsOnCancelDuringBackoff(t *testing.T) {
	// R40b: the child crashed and so did not revert; ctx ends while the
	// supervisor waits to restart it.
	c := newChild(t)
	j, r := pendingJournal(t)
	r.onCall = func() {
		if !c.lastExited() {
			t.Error("reverter called while the child is alive")
		}
	}
	clk := &blockingClock{fakeClock: newClock(), waits: make(chan time.Duration, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan result, 1)
	go func() {
		out <- result{supervisor.Run(ctx, c.factory(-1, ""), j, map[string]journal.Reverter{"fake": r}, clk)}
	}()
	select {
	case d := <-clk.waits:
		if d != time.Second {
			t.Fatalf("first backoff = %v, want 1s", d)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no backoff wait")
	}
	cancel()
	if err := wait(t, out); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if c.starts() != 1 {
		t.Fatalf("starts = %d, want 1 (no restart after cancel)", c.starts())
	}
	if r.Restores() != 1 || pendingCount(t, j) != 0 {
		t.Fatalf("restores %d, pending %d; want the crashed child's entry reverted", r.Restores(), pendingCount(t, j))
	}
}
