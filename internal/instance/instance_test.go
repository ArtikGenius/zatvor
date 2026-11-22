package instance_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/api"
	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/instance"
	"github.com/ArtikGenius/zatvor/internal/settings"
)

// testName is a lock name that never collides with a real instance: a unique
// mutex base name on Windows, a lock file in a temp dir elsewhere.
func testName(t *testing.T) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("ZatvorTest-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return filepath.Join(t.TempDir(), "instance.lock")
}

// showBackend counts Show calls; every other Backend method is unused.
type showBackend struct {
	api.Backend
	mu    sync.Mutex
	shows int
}

func (b *showBackend) Settings() settings.Settings { return settings.Defaults() }
func (b *showBackend) Show()                       { b.mu.Lock(); b.shows++; b.mu.Unlock() }
func (b *showBackend) count() int                  { b.mu.Lock(); defer b.mu.Unlock(); return b.shows }

func TestSecondInstanceForwardsShow(t *testing.T) {
	name := testName(t)
	release, already, err := instance.Acquire(name)
	if err != nil || already {
		t.Fatalf("first Acquire: already=%v err=%v", already, err)
	}
	release2, already2, err := instance.Acquire(name)
	if err != nil || !already2 {
		t.Fatalf("second Acquire: already=%v err=%v, want already", already2, err)
	}
	release2() // a no-op: it must not drop the first instance's lock
	if _, again, err := instance.Acquire(name); err != nil || !again {
		t.Fatalf("after the second's release: already=%v err=%v, want still held", again, err)
	}

	dirs, err := appdir.Resolve(filepath.Join(t.TempDir(), "Zatvor"))
	if err != nil {
		t.Fatal(err)
	}
	b := &showBackend{}
	srv := api.New(b, dirs)
	if _, err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := instance.Forward(dirs); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if n := b.count(); n != 1 {
		t.Errorf("Show called %d times, want 1", n)
	}

	release()
	release() // idempotent
	r3, already3, err := instance.Acquire(name)
	if err != nil || already3 {
		t.Fatalf("Acquire after release: already=%v err=%v", already3, err)
	}
	r3()
}

func TestForwardWithoutDiscovery(t *testing.T) {
	dirs, err := appdir.Resolve(filepath.Join(t.TempDir(), "Zatvor"))
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Forward(dirs); err == nil {
		t.Error("Forward succeeded with no discovery file")
	}
}

func TestForwardRejectsBadToken(t *testing.T) {
	dirs, err := appdir.Resolve(filepath.Join(t.TempDir(), "Zatvor"))
	if err != nil {
		t.Fatal(err)
	}
	b := &showBackend{}
	srv := api.New(b, dirs)
	if _, err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := os.WriteFile(filepath.Join(dirs.Run, api.TokenFile), []byte(strings.Repeat("0", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.Forward(dirs); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("Forward with a stale token = %v, want an HTTP 403 error", err)
	}
	if b.count() != 0 {
		t.Error("Show ran with a wrong token")
	}
}

func TestNameUsesRunDir(t *testing.T) {
	dirs := appdir.Dirs{Run: filepath.Join("base", "run")}
	got := instance.Name(dirs)
	want := filepath.Join(dirs.Run, "instance.lock")
	if runtime.GOOS == "windows" {
		want = "Zatvor"
	}
	if got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}
