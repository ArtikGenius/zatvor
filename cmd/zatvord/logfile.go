package main

import (
	"os"
	"sync"
)

// logMax is the size at which zatvord.log moves to zatvord.log.1 (R44).
const logMax = 5 << 20

// rotatingLog appends to a log file that the supervisor and its child both
// write. Each Write opens the file, checks its size, moves it to "<path>.1"
// (replacing the old one) when the write would pass max, appends and closes
// it again. No process keeps the file open, so the rename also works on
// Windows, and both processes see the same size. A failed rename only
// delays the rotation to a later write.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
}

func newRotatingLog(path string, max int64) *rotatingLog { return &rotatingLog{path: path, max: max} }

func (l *rotatingLog) Write(p []byte) (int, error) {
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

// teeLog writes every line to each writer and ignores their errors: a
// missing console (stderr) must not keep lines from the file, nor the
// other way round.
type teeLog []interface{ Write([]byte) (int, error) }

func (t teeLog) Write(p []byte) (int, error) {
	for _, w := range t {
		_, _ = w.Write(p)
	}
	return len(p), nil
}
