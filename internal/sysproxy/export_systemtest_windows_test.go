//go:build systemtest && windows

package sysproxy

// FlagsOurs is the flags value Ours writes: direct plus the manual proxy.
const FlagsOurs = proxyTypeDirect | proxyTypeProxy

// RawFlags reads the connection's FLAGS and FLAGS_UI options; uiErr is set
// where FLAGS_UI is not supported.
func RawFlags(name string) (flags, ui uint32, uiErr, err error) {
	if flags, err = queryFlags(name, perConnFlags); err != nil {
		return 0, 0, nil, err
	}
	ui, uiErr = queryFlags(name, perConnFlagsUI)
	return flags, ui, uiErr, nil
}
