//go:build windows

package daemon

import (
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/ArtikGenius/zatvor/internal/appdir"
	"github.com/ArtikGenius/zatvor/internal/safemode"
	"github.com/ArtikGenius/zatvor/internal/store"
	"github.com/ArtikGenius/zatvor/internal/sysapi"
	"github.com/ArtikGenius/zatvor/internal/sysproxy"
)

// liveSupported: live mode exists on Windows only (R3, ADR 0004).
const liveSupported = true

// keyProvider is the key of the mode's store: a DPAPI-sealed file for the
// safe store; Credential Manager for the live store, after a one-time move
// of a DPAPI key file of the live store (R17). Safe mode never touches
// Credential Manager.
func keyProvider(mode safemode.Mode, dirs appdir.Dirs, logf func(string, ...any)) (store.KeyProvider, error) {
	path := keyPath(mode, dirs)
	if mode != safemode.Live {
		return store.DPAPIFileKey(path), nil
	}
	target := credTarget(dirs)
	moved, err := store.MigrateDPAPIToCredMan(path, target)
	if err != nil {
		return nil, err
	}
	if moved {
		logf("store: the key moved from the DPAPI file to Credential Manager")
	}
	return store.CredManKey(target), nil
}

func liveSystemProxy() (sysapi.SystemProxy, error) { return sysproxy.NewWindows(), nil }

// defaultDevRoots are the current user's profile, Documents (which may be
// redirected, so it is asked from the shell) and roaming AppData.
func defaultDevRoots(env func(string) string) devRoots {
	docs, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
	if err != nil {
		docs = ""
		if h := env("USERPROFILE"); h != "" {
			docs = filepath.Join(h, "Documents")
		}
	}
	return devRoots{Home: env("USERPROFILE"), Documents: docs, AppData: env("APPDATA")}
}
