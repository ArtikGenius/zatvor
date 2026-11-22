// Package rotlog is an append-only log file that moves to "<path>.1" at a
// size limit, keeping one old file (R44). zatvord.log and safe-mode.jsonl
// use it.
package rotlog

import (
	"os"
	"sync"
)

// Max is the size at which the daemon's logs move to "<path>.1" (R44).
const Max = 5 << 20

// File appends to a log file that several processes (the supervisor and
// its child) may write. Each Write opens the file, checks its size, moves
// it to "<path>.1" (replacing the old one) when the write would pass max,
// appends and closes it again. No process keeps the file open, so the
// rename also works on Windows, and every process sees the same size. A
// failed rename only delays the rotation to a later write.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
}

// New returns the log at path with the limit max.
func New(path string, max int64) *File { return &File{path: path, max: max} }

func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > 0 && fi.Size()+int64(len(p)) > l.max {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := f.Write(p)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}
