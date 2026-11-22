//go:build darwin

package hwid

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"time"
)

var platformUUID = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

// MachineID reads IOPlatformUUID via ioreg (read-only).
func MachineID() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return "", err
	}
	m := platformUUID.FindSubmatch(out)
	if m == nil {
		return "", errors.New("hwid: IOPlatformUUID not found")
	}
	return string(m[1]), nil
}
