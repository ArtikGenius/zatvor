//go:build linux || darwin

package testguard

import "fmt"

// procInfo reports a running process. Zombies count as exited.
func procInfo(pid int) (osInfo, error) {
	if pid <= 0 {
		return osInfo{}, fmt.Errorf("invalid pid %d", pid)
	}
	info, zombie, err := readProc(pid)
	if err != nil {
		return osInfo{}, err
	}
	if zombie {
		return osInfo{}, errExited
	}
	return info, nil
}
