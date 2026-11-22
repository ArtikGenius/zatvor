//go:build windows

package instance

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"

	"github.com/ArtikGenius/zatvor/internal/appdir"
)

// Name is the lock name of the daemon: "Zatvor", for the mutex
// Local\Zatvor-<user SID>. It is per user and per session, not per data dir.
func Name(appdir.Dirs) string { return "Zatvor" }

// acquire creates the named mutex Local\<name>-<current user SID>. The
// mutex exists while a handle to it is open: the handle is kept until
// release. It is not inheritable, so a child (Xray) never keeps it alive.
// ERROR_ACCESS_DENIED counts as already running: an elevated instance of the
// same user holds the mutex with a DACL a non-elevated process cannot open.
func acquire(name string) (func(), bool, error) {
	if name == "" || strings.ContainsRune(name, '\\') {
		return nil, false, fmt.Errorf("instance: invalid mutex name %q", name)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, false, fmt.Errorf("instance: current user: %w", err)
	}
	full, err := windows.UTF16PtrFromString(`Local\` + name + "-" + user.User.Sid.String())
	if err != nil {
		return nil, false, fmt.Errorf("instance: %w", err)
	}
	h, err := windows.CreateMutex(nil, false, full)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return func() {}, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("instance: create mutex: %w", err)
	}
	var once sync.Once
	return func() { once.Do(func() { windows.CloseHandle(h) }) }, false, nil
}
