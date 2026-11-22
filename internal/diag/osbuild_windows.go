package diag

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// osBuild is the Windows version as major.minor.build.
func osBuild() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}
