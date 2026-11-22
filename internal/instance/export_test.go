package instance

// SetAfterTokenRead installs a hook that Forward runs between reading the
// token and re-reading the discovery file; it returns the restore func.
func SetAfterTokenRead(f func()) (restore func()) {
	prev := afterTokenRead
	afterTokenRead = f
	return func() { afterTokenRead = prev }
}
