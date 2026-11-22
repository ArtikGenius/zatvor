package api

import "time"

// SetSSEWriteTimeout changes the per-write deadline of event streams and
// returns a func that restores it.
func SetSSEWriteTimeout(d time.Duration) (restore func()) {
	prev := sseWriteTimeout
	sseWriteTimeout = d
	return func() { sseWriteTimeout = prev }
}

// SetSSEKeepAlive changes the keep-alive interval of event streams and
// returns a func that restores it.
func SetSSEKeepAlive(d time.Duration) (restore func()) {
	prev := keepAlive
	keepAlive = d
	return func() { keepAlive = prev }
}
