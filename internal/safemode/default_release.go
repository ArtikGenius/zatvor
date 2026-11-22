//go:build release

package safemode

// Release builds are safe by default too, until a stage actually ships
// releases (ADR 0004: revisit when release builds exist).
const (
	releaseBuild   = true
	buildDefault   = Safe
	liveNeedsAllow = true
)
