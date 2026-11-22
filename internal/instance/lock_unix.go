//go:build unix

package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/ArtikGenius/zatvor/internal/appdir"
)

// Name is the lock name of the daemon that uses dirs: the lock file
// <Run>/instance.lock.
func Name(dirs appdir.Dirs) string { return filepath.Join(dirs.Run, "instance.lock") }

// acquire takes flock(LOCK_EX|LOCK_NB) on the file name. The file is never
// deleted: removing a lock file races with a new holder. A lock belongs to
// the open file, so a second Acquire in the same process also sees already.
func acquire(name string) (func(), bool, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("instance: open lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return func() {}, true, nil
		}
		return nil, false, fmt.Errorf("instance: lock: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unix.Flock(int(f.Fd()), unix.LOCK_UN)
			f.Close()
		})
	}, false, nil
}
