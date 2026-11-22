//go:build !windows

package xrayrun

import "os"

// assignJob ties the child's lifetime to this process on Windows only.
// Elsewhere it does nothing.
func assignJob(*os.Process) error { return nil }
