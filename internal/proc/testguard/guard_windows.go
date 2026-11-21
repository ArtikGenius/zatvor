package testguard

import (
	"fmt"
	"math"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procInfo opens the process with query-limited and synchronize rights only
// and reads, through that one handle, whether it still runs
// (WaitForSingleObject), its creation time (GetProcessTimes, 100 ns
// resolution) and its parent (NtQueryInformationProcess,
// ProcessBasicInformation.InheritedFromUniqueProcessId).
func procInfo(pid int) (osInfo, error) {
	if pid <= 0 || pid > math.MaxUint32 {
		return osInfo{}, fmt.Errorf("invalid pid %d", pid)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return osInfo{}, fmt.Errorf("process not found: %w", err)
	}
	defer windows.CloseHandle(h)

	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return osInfo{}, fmt.Errorf("wait: %w", err)
	}
	if ev != uint32(windows.WAIT_TIMEOUT) {
		return osInfo{}, errExited
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return osInfo{}, fmt.Errorf("GetProcessTimes: %w", err)
	}
	var pbi windows.PROCESS_BASIC_INFORMATION
	var n uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &n); err != nil {
		return osInfo{}, fmt.Errorf("NtQueryInformationProcess: %w", err)
	}
	return osInfo{ppid: int(pbi.InheritedFromUniqueProcessId), created: time.Unix(0, created.Nanoseconds())}, nil
}
