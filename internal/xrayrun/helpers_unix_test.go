//go:build !windows

package xrayrun

import (
	"net"
	"os"
	"os/signal"
	"syscall"
	"testing"
)

func ignoreInterrupt() { signal.Ignore(os.Interrupt) }

// alive reports whether pid exists and is not a zombie we could still reap.
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err == nil {
		// state is the field after the ")" of the command name
		for i := len(b) - 1; i > 0; i-- {
			if b[i] == ')' && i+2 < len(b) {
				return b[i+2] != 'Z' && b[i+2] != 'X'
			}
		}
	}
	return syscall.Kill(pid, 0) == nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// helperMain has no helper modes outside Windows.
func helperMain() (int, bool) { return 0, false }
