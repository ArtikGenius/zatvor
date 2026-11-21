package testguard

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// sZomb is SZOMB from <sys/proc.h>.
const sZomb = 5

// readProc uses sysctl kern.proc.pid: Eproc.Ppid and Proc.P_starttime
// (microsecond resolution).
func readProc(pid int) (osInfo, bool, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return osInfo{}, false, fmt.Errorf("process not found: %w", err)
	}
	if int(k.Proc.P_pid) != pid {
		return osInfo{}, false, fmt.Errorf("process not found: sysctl returned pid %d", k.Proc.P_pid)
	}
	st := k.Proc.P_starttime
	created := time.Unix(int64(st.Sec), int64(st.Usec)*int64(time.Microsecond))
	return osInfo{ppid: int(k.Eproc.Ppid), created: created}, k.Proc.P_stat == sZomb, nil
}
