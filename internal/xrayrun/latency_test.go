package xrayrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/ojson"
)

func latencyOutbounds(t *testing.T, n int) []*ojson.Object {
	t.Helper()
	var outs []*ojson.Object
	for i := 0; i < n; i++ {
		o := &ojson.Object{}
		o.Set("tag", "o"+string(rune('a'+i)))
		o.Set("protocol", "freedom")
		outs = append(outs, o)
	}
	return outs
}

func runs(t *testing.T, dir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "run\n")
}

// TestLatencyFallbackOnlyForConfigFailures: an Xray that exits at start
// (as on a rejected outbound) triggers one run per outbound; a missing
// binary and a cancelled ctx do not.
func TestLatencyFallbackOnlyForConfigFailures(t *testing.T) {
	outs := latencyOutbounds(t, 3)

	t.Run("exit at start", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "mode"), []byte("crashcount"), 0o600)
		res := Latency(context.Background(), os.Args[0], dir, outs, "http://127.0.0.1:1/", time.Second)
		if n := runs(t, dir); n != 4 {
			t.Fatalf("%d runs, want 4 (shared + one per outbound)", n)
		}
		for _, r := range res {
			if !errors.Is(r.Err, ErrExited) {
				t.Errorf("%s: %v", r.Tag, r.Err)
			}
		}
	})
	t.Run("missing binary", func(t *testing.T) {
		res := Latency(context.Background(), filepath.Join(t.TempDir(), "no-xray"), t.TempDir(), outs, "", time.Second)
		for _, r := range res {
			if !errors.Is(r.Err, ErrSpawn) || r.Err != res[0].Err {
				t.Errorf("%s: %v, want the one shared ErrSpawn", r.Tag, r.Err)
			}
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "mode"), []byte("sleepcount"), 0o600)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := Latency(ctx, os.Args[0], dir, outs, "", time.Second)
		if n := runs(t, dir); n > 1 {
			t.Fatalf("%d runs, want at most the shared one", n)
		}
		for _, r := range res {
			if r.Err == nil || r.Err != res[0].Err {
				t.Errorf("%s: %v, want the one shared error", r.Tag, r.Err)
			}
		}
	})
}
