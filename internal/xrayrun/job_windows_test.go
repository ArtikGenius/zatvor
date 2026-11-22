//go:build windows

package xrayrun

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// jobParent is the helper parent process (see helperMain).
func jobParent(dir string) int {
	r := New(os.Args[0], dir, nil)
	if err := r.Start(context.Background(), []byte("{}"), nil); err != nil {
		os.Stderr.WriteString("start: " + err.Error() + "\n")
		return 2
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

// TestJobObjectKillsXray: a parent process (this test binary in helper
// mode) starts a fake xray through Runner; when the parent is killed, the
// job object closes and the child must exit too. Only processes this test
// started are touched: the parent through its exec handle, the child
// through a handle opened from the PID our own parent reported.
func TestJobObjectKillsXray(t *testing.T) {
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
	// The parent is alive and has not waited on the child yet, so the PID
	// still names our grandchild.
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	defer windows.CloseHandle(h)
	if !alive(pid) {
		t.Fatal("child is not running before the parent is killed")
	}

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
