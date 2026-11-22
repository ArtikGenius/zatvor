//go:build !windows

package api

import "syscall"

// listenControl has nothing to add outside Windows: a bound port cannot be
// taken over by another socket without SO_REUSEPORT on both.
var listenControl func(network, address string, c syscall.RawConn) error
