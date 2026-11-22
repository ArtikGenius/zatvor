package supervisor

import "time"

// SetStopTimeout changes how long a stopped child may take; it returns the
// restore func.
func SetStopTimeout(d time.Duration) (restore func()) {
	prev := stopTimeout
	stopTimeout = d
	return func() { stopTimeout = prev }
}

// SetAfterStart installs a hook run right after each child started; it
// returns the restore func.
func SetAfterStart(f func()) (restore func()) {
	prev := afterStart
	afterStart = f
	return func() { afterStart = prev }
}
