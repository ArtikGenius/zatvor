package api

import "time"

// SetSSEWriteTimeout changes the per-write deadline of event streams and
// returns a func that restores it.
func SetSSEWriteTimeout(d time.Duration) (restore func()) {
	prev := sseWriteTimeout
	sseWriteTimeout = d
	return func() { sseWriteTimeout = prev }
}
