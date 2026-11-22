package ports

import (
	"errors"
	"math/rand"
	"syscall"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/gen"
)

func okProbe(int) error { return nil }

func rnd() *rand.Rand { return rand.New(rand.NewSource(1)) }

func TestKeepsWorkingPorts(t *testing.T) {
	prev := gen.Ports{Mixed: 21000, Probe: 30000, DevTools: 40000, API: 50000}
	got, changed, err := Pick(prev, okProbe, rnd())
	if err != nil || changed || got != prev {
		t.Fatalf("got %+v changed=%v err=%v", got, changed, err)
	}
}

func TestRepicksOnlyTheBusyOne(t *testing.T) {
	for _, e := range []error{syscall.EADDRINUSE, syscall.EACCES, errors.New("wsaeacces")} {
		prev := gen.Ports{Mixed: 21000, Probe: 30000, DevTools: 40000, API: 50000}
		probe := func(p int) error {
			if p == 30000 {
				return e
			}
			return nil
		}
		got, changed, err := Pick(prev, probe, rnd())
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if got.Mixed != prev.Mixed || got.DevTools != prev.DevTools || got.API != prev.API {
			t.Fatalf("others changed: %+v", got)
		}
		if got.Probe == 30000 || got.Probe < 20000 {
			t.Fatalf("probe not repicked: %d", got.Probe)
		}
		for _, k := range []int{prev.Mixed, prev.DevTools, prev.API} {
			if d := got.Probe - k; d < 1024 && d > -1024 {
				t.Fatalf("repicked port too close to kept %d: %d", k, got.Probe)
			}
		}
	}
}

func TestZeroPortIsPickedFresh(t *testing.T) {
	prev := gen.Ports{Mixed: 21000, Probe: 30000, DevTools: 40000}
	got, changed, err := Pick(prev, okProbe, rnd())
	if err != nil || !changed || got.API == 0 || got.Mixed != 21000 {
		t.Fatalf("got %+v changed=%v err=%v", got, changed, err)
	}
}

func TestAvoidsWellKnown(t *testing.T) {
	bad := map[int]bool{10808: true, 10809: true, 7890: true, 9090: true, 9097: true, 19090: true}
	for seed := int64(0); seed < 300; seed++ {
		got, _, err := Pick(gen.Ports{}, okProbe, rand.New(rand.NewSource(seed)))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []int{got.Mixed, got.Probe, got.DevTools, got.API} {
			if bad[p] || p < 20000 || p > 60999 {
				t.Fatalf("seed %d: bad port %d", seed, p)
			}
		}
	}
}

func TestSpread(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		got, _, err := Pick(gen.Ports{}, okProbe, rand.New(rand.NewSource(seed)))
		if err != nil {
			t.Fatal(err)
		}
		ps := []int{got.Mixed, got.Probe, got.DevTools, got.API}
		for i := range ps {
			for j := i + 1; j < len(ps); j++ {
				if d := ps[i] - ps[j]; d < 1024 && d > -1024 {
					t.Fatalf("seed %d: %v too close", seed, ps)
				}
			}
		}
	}
}

func TestDuplicatePrevIsRepicked(t *testing.T) {
	prev := gen.Ports{Mixed: 21000, Probe: 21000, DevTools: 40000, API: 50000}
	got, changed, err := Pick(prev, okProbe, rnd())
	if err != nil || !changed || got.Mixed == got.Probe {
		t.Fatalf("got %+v changed=%v err=%v", got, changed, err)
	}
}

func TestExhausted(t *testing.T) {
	calls := 0
	probe := func(int) error { calls++; return syscall.EADDRINUSE }
	_, _, err := Pick(gen.Ports{}, probe, rnd())
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("err = %v", err)
	}
	if calls > 64 {
		t.Fatalf("%d tries", calls)
	}
}

func TestLoopbackProber(t *testing.T) {
	got, _, err := Pick(gen.Ports{}, LoopbackProber, rand.New(rand.NewSource(7)))
	if err != nil {
		t.Skipf("no free ports in sandbox: %v", err)
	}
	if err := LoopbackProber(got.Mixed); err != nil {
		t.Fatalf("picked port not bindable: %v", err)
	}
}
