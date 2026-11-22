package appdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SettingsFile is the daemon's settings file in Dirs.Data.
const SettingsFile = "settings.json"

// Home is the user's home directory as env reports it: USERPROFILE on
// Windows, HOME elsewhere. env looks up an environment variable, like
// os.Getenv.
func Home(env func(string) string) string {
	if runtime.GOOS == "windows" {
		return env("USERPROFILE")
	}
	return env("HOME")
}

// IsDataDir reports whether dir looks like a Zatvor data directory: it has
// the settings file, or both the journal and run subdirectories.
func IsDataDir(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, SettingsFile)); err == nil && fi.Mode().IsRegular() {
		return true
	}
	for _, sub := range []string{"journal", "run"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// ValidateDataDir checks a --data-dir before Resolve tightens its
// permissions: it must be absolute, and neither a filesystem root, nor the
// home directory itself, nor an existing non-empty directory that is not a
// Zatvor data directory. zatvord and zatvor both use it.
func ValidateDataDir(path, home string) error {
	if !filepath.IsAbs(path) {
		return errors.New("the data directory must be an absolute path")
	}
	p := filepath.Clean(path)
	if filepath.Dir(p) == p {
		return errors.New("the data directory must not be a filesystem root")
	}
	if home != "" && samePath(p, filepath.Clean(home)) {
		return errors.New("the data directory must not be the home directory itself")
	}
	entries, err := os.ReadDir(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("the data directory cannot be read: %w", err)
	case len(entries) == 0 || IsDataDir(p):
		return nil
	}
	return errors.New("the data directory is a non-empty directory that is not a Zatvor data directory")
}

func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
