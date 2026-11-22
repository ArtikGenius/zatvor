// Package appdir resolves and creates the daemon's per-user directories. They
// are readable by the owner only: mode 0700, or on Windows a protected DACL
// with a single entry for the current user.
package appdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Dirs are the daemon's directories. Data is the base directory; the others
// are its subdirectories.
type Dirs struct {
	Data, Cache, Logs, Journal, Geo, Run string
}

// Resolve creates base and its subdirectories for the owner only, and returns
// them. It is safe to call on existing directories; their permissions are
// tightened.
func Resolve(base string) (Dirs, error) {
	if base == "" {
		return Dirs{}, errors.New("appdir: empty base directory")
	}
	d := Dirs{
		Data:    base,
		Cache:   filepath.Join(base, "cache"),
		Logs:    filepath.Join(base, "logs"),
		Journal: filepath.Join(base, "journal"),
		Geo:     filepath.Join(base, "geo"),
		Run:     filepath.Join(base, "run"),
	}
	for _, p := range []string{d.Data, d.Cache, d.Logs, d.Journal, d.Geo, d.Run} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return Dirs{}, fmt.Errorf("appdir: %w", err)
		}
		if err := restrict(p); err != nil {
			return Dirs{}, fmt.Errorf("appdir: restrict %s: %w", p, err)
		}
	}
	return d, nil
}

// Default returns the per-user base directory for this OS: %LOCALAPPDATA%\Zatvor
// on Windows, ~/Library/Application Support/Zatvor on macOS and
// $XDG_DATA_HOME/zatvor (default ~/.local/share/zatvor) elsewhere. env looks up
// an environment variable, like os.Getenv.
func Default(env func(string) string) (string, error) {
	return defaultFor(runtime.GOOS, env)
}

func defaultFor(goos string, env func(string) string) (string, error) {
	switch goos {
	case "windows":
		if v := env("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "Zatvor"), nil
		}
		return "", errors.New("appdir: LOCALAPPDATA is not set")
	case "darwin":
		if h := env("HOME"); h != "" {
			return filepath.Join(h, "Library", "Application Support", "Zatvor"), nil
		}
		return "", errors.New("appdir: HOME is not set")
	default:
		if v := env("XDG_DATA_HOME"); v != "" {
			return filepath.Join(v, "zatvor"), nil
		}
		if h := env("HOME"); h != "" {
			return filepath.Join(h, ".local", "share", "zatvor"), nil
		}
		return "", errors.New("appdir: neither XDG_DATA_HOME nor HOME is set")
	}
}
