//go:build windows

package xrayrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobParent is the helper parent process (see helperMain). It exits
// non-zero when the child could not be put into the job object (the
// Started event carries the error).
func jobParent(dir string) int {
	r := New(os.Args[0], dir, nil)
	if err := r.Start(context.Background(), []byte("{}"), nil); err != nil {
		os.Stderr.WriteString("start: " + err.Error() + "\n")
		return 2
	}
	select {
	case ev := <-r.Events():
		if ev.Kind != Started || ev.Err != nil {
			os.Stderr.WriteString(fmt.Sprintf("first event %v: %v\n", ev.Kind, ev.Err))
			return 4
		}
	case <-time.After(10 * time.Second):
		os.Stderr.WriteString("no Started event\n")
		return 4
	}
	tmp := filepath.Join(dir, "child.pid.tmp")
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(r.Pid())), 0o600); err != nil {
		return 3
	}
	if err := os.Rename(tmp, filepath.Join(dir, "child.pid")); err != nil {
		return 3
	}
	_, _ = io.Copy(io.Discard, os.Stdin) // until the test closes stdin or kills us
	return 0
}

// openVerified opens pid and keeps the handle only if, read through that
// same handle, the process still runs, its parent is wantParent and it was
// created at or after notBefore. A PID that was reused fails the check, so
// the test never acts on a process it did not start.
func openVerified(pid, wantParent int, notBefore time.Time) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	fail := func(err error) (windows.Handle, error) { windows.CloseHandle(h); return 0, err }
	if ev, err := windows.WaitForSingleObject(h, 0); err != nil || ev != uint32(windows.WAIT_TIMEOUT) {
		return fail(fmt.Errorf("not running (wait=%#x err=%v)", ev, err))
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return fail(fmt.Errorf("GetProcessTimes: %w", err))
	}
	if c := time.Unix(0, created.Nanoseconds()); c.Before(notBefore) {
		return fail(fmt.Errorf("created %v, before the test started (%v)", c, notBefore))
	}
	var pbi windows.PROCESS_BASIC_INFORMATION
	var n uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &n); err != nil {
		return fail(fmt.Errorf("NtQueryInformationProcess: %w", err))
	}
	if got := int(pbi.InheritedFromUniqueProcessId); got != wantParent {
		return fail(fmt.Errorf("parent is %d, want our helper %d", got, wantParent))
	}
	return h, nil
}

// TestJobObjectKillsXray: a parent process (this test binary in helper
// mode) starts a fake xray through Runner; when the parent is killed, the
// job object closes and the child must exit too. The helper is a re-exec of
// this test binary (testguard.Spawn runs a different child; ruling R25).
// Only processes this test started are touched: the parent through its
// exec handle, the child through one handle opened from the PID our parent
// reported and verified (running, parent = our helper, created after the
// test started) before it is used.
func TestJobObjectKillsXray(t *testing.T) {
	start := time.Now().Add(-time.Second) // margin for clock granularity
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("sleep"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(os.Args[0], jobParentArg, dir)
	stdin, err := parent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	parent.Stderr = &stderr
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	parentDone := make(chan struct{})
	go func() { _ = parent.Wait(); close(parentDone) }()
	defer func() {
		stdin.Close()
		_ = parent.Process.Kill()
		<-parentDone
	}()

	var pid int
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(dir, "child.pid"))
		if err == nil {
			if pid, err = strconv.Atoi(string(b)); err != nil || pid <= 0 {
				t.Fatalf("bad child pid %q", b)
			}
			break
		}
		select {
		case <-parentDone:
			t.Fatalf("parent exited early: %s", stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("parent never reported its child")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Open the grandchild at once and verify it through that handle; only
	// that handle is used from here on, for waiting and for cleanup.
	h, err := openVerified(pid, parent.Process.Pid, start)
	if err != nil {
		t.Fatalf("child %d: %v", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-parentDone
	ev, err := windows.WaitForSingleObject(h, 15_000)
	if err != nil || ev != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(h, 1) // our own grandchild, by handle
		t.Fatalf("child still running after the parent was killed (wait=%#x err=%v)", ev, err)
	}
}
