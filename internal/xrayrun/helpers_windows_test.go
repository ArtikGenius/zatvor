//go:build windows

package xrayrun

import (
	"net"
	"os"
	"os/signal"
	"testing"
)

func ignoreInterrupt() { signal.Ignore(os.Interrupt) }

// alive: on Windows a killed process cannot be reopened.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}
