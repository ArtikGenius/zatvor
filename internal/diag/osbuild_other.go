//go:build !windows

package diag

// osBuild is reported on Windows only.
func osBuild() string { return "" }
