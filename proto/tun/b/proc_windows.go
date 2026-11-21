//go:build windows

package main

import (
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpT = iphlpapi.NewProc("GetExtendedTcpTable")
)

const tcpTableOwnerPIDAll = 5

// tcpOwnerPath returns the executable path of the process that owns the local
// TCP endpoint local (the source of a flow seen on the TUN).
func tcpOwnerPath(local netip.AddrPort) (string, error) {
	pid, err := tcpOwnerPID(local)
	if err != nil {
		return "", err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("OpenProcess %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", fmt.Errorf("QueryFullProcessImageName %d: %w", pid, err)
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func tcpOwnerPID(local netip.AddrPort) (uint32, error) {
	addr := local.Addr().Unmap()
	family, rowSize, addrOff, addrLen, portOff, pidOff := uint32(windows.AF_INET), 24, 4, 4, 8, 20
	if addr.Is6() {
		// MIB_TCP6ROW_OWNER_PID
		family, rowSize, addrOff, addrLen, portOff, pidOff = windows.AF_INET6, 56, 0, 16, 20, 52
	}
	var size uint32
	var buf []byte
	for range 5 {
		var p unsafe.Pointer
		if len(buf) > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := procGetExtendedTcpT.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
		if r == 0 {
			break
		}
		if windows.Errno(r) != windows.ERROR_INSUFFICIENT_BUFFER {
			return 0, fmt.Errorf("GetExtendedTcpTable: %w", windows.Errno(r))
		}
		buf = make([]byte, size)
	}
	if len(buf) < 4 {
		return 0, errors.New("GetExtendedTcpTable: empty")
	}
	n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
	want := addr.AsSlice()
	for i := range n {
		row := buf[4+i*rowSize : 4+(i+1)*rowSize]
		port := uint16(row[portOff])<<8 | uint16(row[portOff+1])
		if port != local.Port() {
			continue
		}
		if string(row[addrOff:addrOff+addrLen]) != string(want) {
			continue
		}
		return *(*uint32)(unsafe.Pointer(&row[pidOff])), nil
	}
	return 0, fmt.Errorf("no TCP owner for %s", local)
}
