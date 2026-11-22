//go:build !windows

package daemon

import (
	"path/filepath"
	"runtime"

	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/safemode"
	"github.com/ArtikGenius/zatvor/internal/store"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
)

// liveSupported: live mode exists on Windows only (R3, ADR 0004).
const liveSupported = false

// keyProvider is store.FileKey in both modes off Windows (R3).
func keyProvider(_ safemode.Mode, dirs appdir.Dirs, _ func(string, ...any)) (store.KeyProvider, error) {
	return store.FileKey(filepath.Join(dirs.Data, KeyFile)), nil
}

func liveSystemProxy() (sysapi.SystemProxy, error) { return nil, ErrLiveRefused }

// defaultDevRoots: VS Code keeps its settings under Application Support on
// macOS and under XDG_CONFIG_HOME (default ~/.config) elsewhere.
func defaultDevRoots(env func(string) string) devRoots {
	home := env("HOME")
	r := devRoots{Home: home}
	if home != "" {
		r.Documents = filepath.Join(home, "Documents")
	}
	switch {
	case runtime.GOOS == "darwin" && home != "":
		r.AppData = filepath.Join(home, "Library", "Application Support")
	case env("XDG_CONFIG_HOME") != "":
		r.AppData = env("XDG_CONFIG_HOME")
	case home != "":
		r.AppData = filepath.Join(home, ".config")
	}
	return r
}
