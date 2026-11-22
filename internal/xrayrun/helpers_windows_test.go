//go:build windows

package xrayrun

import (
	"net"
	"os"
	"os/signal"
	"testing"

	"golang.org/x/sys/windows"
)

func ignoreInterrupt() { signal.Ignore(os.Interrupt) }

// alive reports whether pid is a process that has not exited: it opens the
// process and checks the exit code (an exited process whose handle is still
// open elsewhere reports an exit code, not STILL_ACTIVE).
func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false // ERROR_INVALID_PARAMETER: no such process
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}
