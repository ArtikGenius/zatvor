//go:build !windows

package appdir

import "os"

// restrict sets mode 0700 (MkdirAll is subject to the umask and skips
// existing directories).
func restrict(path string) error { return os.Chmod(path, 0o700) }
