package freeze

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

// These ids are all refused before any OS call, so the test is safe anywhere.
func TestRefusesBeforeOSCall(t *testing.T) {
	called := false
	f := func(sysapi.ProcID) error { called = true; return nil }
	for _, tc := range []struct {
		id   sysapi.ProcID
		want string
	}{
		{sysapi.ProcID{}, "not a valid pid"},
		{sysapi.ProcID{PID: -1, Created: time.Now()}, "not a valid pid"},
		{sysapi.ProcID{PID: os.Getpid(), Created: time.Now()}, "this process"},
		{sysapi.ProcID{PID: os.Getpid() + 1}, "no creation time"},
	} {
		err := act(tc.id, "suspend", f)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("act(%+v) = %v, want %q", tc.id, err, tc.want)
		}
	}
	if called {
		t.Fatal("OS call reached for a refused id")
	}
}

func TestUnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("implemented on windows; covered by the systemtest")
	}
	id := sysapi.ProcID{PID: os.Getpid() + 1, Created: time.Now()}
	for name, f := range map[string]func(sysapi.ProcID) error{"Suspend": Suspend, "Resume": Resume} {
		if err := f(id); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s = %v, want ErrUnsupported", name, err)
		}
	}
}
