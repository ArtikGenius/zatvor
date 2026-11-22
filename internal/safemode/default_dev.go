//go:build !release

package safemode

const (
	releaseBuild   = false
	buildDefault   = Safe
	liveNeedsAllow = true
)
