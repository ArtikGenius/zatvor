//go:build windows

package hwid

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// MachineID reads HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid read-only.
// KEY_WOW64_64KEY keeps a 32-bit process on the same value as a 64-bit one.
func MachineID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", errors.New("hwid: empty MachineGuid")
	}
	return v, nil
}
