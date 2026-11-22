//go:build windows

package api

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// soExclusiveAddrUse is SO_EXCLUSIVEADDRUSE (~SO_REUSEADDR), missing from
// x/sys/windows.
const soExclusiveAddrUse = ^windows.SO_REUSEADDR

// listenControl sets SO_EXCLUSIVEADDRUSE, so that no other socket of the same
// user can bind our port with SO_REUSEADDR and receive the clients' token.
func listenControl(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soExclusiveAddrUse, 1)
	}); err != nil {
		return err
	}
	return serr
}
