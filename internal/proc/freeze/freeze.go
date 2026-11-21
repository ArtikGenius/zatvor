// Package freeze suspends and resumes whole processes.
//
// A process is named by sysapi.ProcID (PID plus creation time). Suspend and
// Resume open one handle to the PID, read the creation time through that
// handle and act on the same handle only if it equals id.Created exactly, so
// a PID reused between the caller's checks and the action is refused.
//
// The package does not decide whether acting is allowed: safe mode and, in
// tests, testguard.Check are the caller's job. Only Windows is implemented
// in stage 0c; other OSes return ErrUnsupported.
package freeze

import (
	"errors"
	"fmt"
	"os"

	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

// ErrUnsupported is returned on OSes without an implementation yet.
var ErrUnsupported = errors.New("freeze: unsupported on this OS in stage 0c")

// Suspend suspends every thread of the process id names.
func Suspend(id sysapi.ProcID) error { return act(id, "suspend", suspend) }

// Resume undoes one Suspend of the process id names.
func Resume(id sysapi.ProcID) error { return act(id, "resume", resume) }

func act(id sysapi.ProcID, what string, f func(sysapi.ProcID) error) error {
	if id.PID <= 0 {
		return fmt.Errorf("freeze: %s pid %d: not a valid pid", what, id.PID)
	}
	if id.PID == os.Getpid() {
		return fmt.Errorf("freeze: %s pid %d: refusing to act on this process", what, id.PID)
	}
	if id.Created.IsZero() {
		return fmt.Errorf("freeze: %s pid %d: no creation time", what, id.PID)
	}
	if err := f(id); err != nil {
		return fmt.Errorf("freeze: %s pid %d: %w", what, id.PID, err)
	}
	return nil
}
