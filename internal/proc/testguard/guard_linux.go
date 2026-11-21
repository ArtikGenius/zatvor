package testguard

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// atClkTck is AT_CLKTCK from <linux/auxvec.h>: the kernel's USER_HZ, which is
// what sysconf(_SC_CLK_TCK) returns and the unit of /proc/<pid>/stat times.
const atClkTck = 17

var (
	clockOnce sync.Once
	clockHz   uint64
	bootSec   int64
	clockErr  error
)

// bootClock returns USER_HZ (from the ELF auxiliary vector) and the boot
// time (btime in /proc/stat, whole seconds). Both are read once per process,
// so every creation time this process computes uses the same base and equal
// tick counts always give equal times.
func bootClock() (uint64, int64, error) {
	clockOnce.Do(func() {
		auxv, err := unix.Auxv()
		if err != nil {
			clockErr = fmt.Errorf("auxv: %w", err)
			return
		}
		for _, kv := range auxv {
			if kv[0] == atClkTck {
				clockHz = uint64(kv[1])
			}
		}
		if clockHz == 0 {
			clockErr = fmt.Errorf("auxv has no AT_CLKTCK")
			return
		}
		b, err := os.ReadFile("/proc/stat")
		if err != nil {
			clockErr = err
			return
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
				bootSec, clockErr = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				return
			}
		}
		clockErr = fmt.Errorf("/proc/stat has no btime")
	})
	return clockHz, bootSec, clockErr
}

// readProc parses /proc/<pid>/stat: field 3 state, 4 ppid, 22 starttime
// (clock ticks since boot).
func readProc(pid int) (osInfo, bool, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return osInfo{}, false, fmt.Errorf("process not found: %w", err)
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // comm may contain spaces and ')'
	if i < 0 {
		return osInfo{}, false, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3
	if len(f) < 20 {
		return osInfo{}, false, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	zombie := f[0] == "Z" || f[0] == "X" || f[0] == "x"
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return osInfo{}, false, fmt.Errorf("/proc/%d/stat ppid: %w", pid, err)
	}
	ticks, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return osInfo{}, false, fmt.Errorf("/proc/%d/stat starttime: %w", pid, err)
	}
	hz, boot, err := bootClock()
	if err != nil {
		return osInfo{}, false, err
	}
	created := time.Unix(boot+int64(ticks/hz), int64(ticks%hz*uint64(time.Second)/hz))
	return osInfo{ppid: ppid, created: created}, zombie, nil
}
