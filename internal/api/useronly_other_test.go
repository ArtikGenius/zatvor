//go:build !windows

package api_test

import (
	"os"
	"testing"
)

// assertUserOnly checks that only the owner can read or write path.
func assertUserOnly(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("%s: mode %v, want 0600", path, got)
	}
}
