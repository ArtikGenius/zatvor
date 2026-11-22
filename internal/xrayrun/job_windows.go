//go:build windows

package xrayrun

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	jobOnce sync.Once
	job     windows.Handle
	jobErr  error
)

// daemonJob is this process's job object for Xray children. It is created
// once with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE and its handle is never
// closed: when this process ends, however it ends, Windows closes the
// handle and kills every process in the job.
func daemonJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil) // not inheritable
		if err != nil {
			jobErr = fmt.Errorf("xrayrun: create job object: %w", err)
			return
		}
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			jobErr = fmt.Errorf("xrayrun: set job object limits: %w", err)
			return
		}
		job = h
	})
	return job, jobErr
}

// assignJob puts the child p into the daemon's job object. It must run
// before anything waits on p: until then os.Process holds the process
// handle, so the PID cannot be reused and names our child.
func assignJob(p *os.Process) error {
	j, err := daemonJob()
	if err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("xrayrun: open child for job object: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(j, h); err != nil {
		return fmt.Errorf("xrayrun: assign child to job object: %w", err)
	}
	return nil
}
