package freeze

import (
	"fmt"
	"math"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

var (
	ntdll                = windows.NewLazySystemDLL("ntdll.dll")
	procNtSuspendProcess = ntdll.NewProc("NtSuspendProcess")
	procNtResumeProcess  = ntdll.NewProc("NtResumeProcess")
)

func suspend(id sysapi.ProcID) error { return onVerifiedHandle(id, procNtSuspendProcess) }
func resume(id sysapi.ProcID) error  { return onVerifiedHandle(id, procNtResumeProcess) }

// onVerifiedHandle opens the process with only suspend/resume and
// query-limited rights, checks its creation time through that handle and
// then calls fn (NtSuspendProcess or NtResumeProcess) on the same handle.
func onVerifiedHandle(id sysapi.ProcID, fn *windows.LazyProc) error {
	if id.PID > math.MaxUint32 {
		return fmt.Errorf("invalid pid")
	}
	h, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(id.PID))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)

	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return fmt.Errorf("GetProcessTimes: %w", err)
	}
	if got := time.Unix(0, created.Nanoseconds()); !got.Equal(id.Created) {
		return fmt.Errorf("creation time %s differs from expected %s (pid reused?)",
			got.Format(time.RFC3339Nano), id.Created.Format(time.RFC3339Nano))
	}
	if err := fn.Find(); err != nil {
		return err
	}
	r, _, _ := fn.Call(uintptr(h))
	if st := windows.NTStatus(r); st != windows.STATUS_SUCCESS {
		return fmt.Errorf("%s: %w", fn.Name, st)
	}
	return nil
}
