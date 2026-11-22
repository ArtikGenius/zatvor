//go:build systemtest && windows

package freeze_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/proc/freeze"
	"github.com/ArtikGenius/zatvor/internal/proc/testguard"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/systest"
)

func TestMain(m *testing.M) { systest.Main(m) }

// frozenFor is how long a suspended child must show no progress.
const frozenFor = time.Second

func TestSuspendResumeOwnChild(t *testing.T) {
	systest.Require(t)
	counter := filepath.Join(t.TempDir(), "counter")
	id := testguard.Spawn(t, "child", "-counter", counter)
	waitAdvance(t, counter, 0, 5*time.Second) // running before we touch it

	if err := testguard.Check(id); err != nil {
		t.Fatalf("testguard.Check(own child) = %v", err)
	}
	if err := freeze.Suspend(id); err != nil {
		t.Fatalf("Suspend(own child) = %v", err)
	}
	resumed := false
	t.Cleanup(func() {
		if !resumed {
			_ = freeze.Resume(id)
		}
	})

	// The child may have been stopped between truncating and writing the
	// file, so compare raw bytes, not parsed numbers.
	before := readRaw(t, counter)
	time.Sleep(frozenFor)
	after := readRaw(t, counter)
	if after != before {
		t.Fatalf("counter changed while suspended: %q -> %q", before, after)
	}
	t.Logf("counter held at %q for %s while suspended", strings.TrimSpace(before), frozenFor)

	if err := testguard.Check(id); err != nil {
		t.Fatalf("testguard.Check(own child) before Resume = %v", err)
	}
	if err := freeze.Resume(id); err != nil {
		t.Fatalf("Resume(own child) = %v", err)
	}
	resumed = true
	last, _ := strconv.Atoi(strings.TrimSpace(before)) // 0 if it was mid-write
	got := waitAdvance(t, counter, last, 5*time.Second)
	t.Logf("counter advanced %d -> %d after Resume", last, got)
}

func TestSuspendRefusesForeign(t *testing.T) {
	systest.Require(t)
	counter := filepath.Join(t.TempDir(), "counter")
	id := testguard.Spawn(t, "child", "-counter", counter)
	start := waitAdvance(t, counter, 0, 5*time.Second)

	// Same PID, different creation time: what a reused PID looks like.
	foreign := sysapi.ProcID{PID: id.PID, Created: id.Created.Add(time.Second)}
	if err := testguard.Check(foreign); err == nil {
		t.Fatal("testguard.Check(foreign) = nil, want refusal")
	} else {
		t.Logf("testguard.Check refused: %v", err)
	}
	// freeze checks the creation time itself, on the handle it acts on.
	err := freeze.Suspend(foreign)
	if err == nil {
		_ = freeze.Resume(id)
		t.Fatal("Suspend(foreign) = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "creation time") {
		t.Fatalf("Suspend(foreign) = %v, want creation time refusal", err)
	}
	t.Logf("Suspend refused: %v", err)
	if err := freeze.Resume(foreign); err == nil {
		t.Fatal("Resume(foreign) = nil, want refusal")
	}

	// The target keeps running.
	got := waitAdvance(t, counter, start, 5*time.Second)
	t.Logf("target kept running: %d -> %d", start, got)

	// A process we did not spawn at all (our parent, the go tool) is refused
	// by the guard; it is never passed to freeze.
	if err := testguard.Check(sysapi.ProcID{PID: os.Getppid(), Created: time.Now()}); err == nil {
		t.Fatal("testguard.Check(parent) = nil, want refusal")
	}
	// Ourselves: freeze refuses outright.
	if err := freeze.Suspend(sysapi.ProcID{PID: os.Getpid(), Created: time.Now()}); err == nil {
		t.Fatal("Suspend(self) = nil, want refusal")
	}
	if err := freeze.Suspend(sysapi.ProcID{}); err == nil {
		t.Fatal("Suspend(zero) = nil, want refusal")
	}
}

func readRaw(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return string(b)
}

// waitAdvance waits until the counter file holds a number greater than
// last and returns it.
func waitAdvance(t *testing.T, path string, last int, wait time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > last {
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("counter in %s did not pass %d within %s", path, last, wait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
