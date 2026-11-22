//go:build linux

package hwid

import (
	"errors"
	"os"
	"strings"
)

// MachineID reads /etc/machine-id (read-only).
func MachineID() (string, error) {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("hwid: empty /etc/machine-id")
	}
	return id, nil
}
