//go:build windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no process-group equivalent with the same semantics as Unix.
// A Job Object gives each task an isolated process tree and lets cancellation
// terminate descendants that a shell or PowerShell may have started.
func configureProcess(command *exec.Cmd) (func(), func() error) {
	command.WaitDelay = time.Second
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return func() {}, command.Run
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return func() {}, command.Run
	}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := windows.TerminateJobObject(job, 1); err != nil {
			if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	run := func() error {
		if err := command.Start(); err != nil {
			return err
		}
		processHandle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
		if err == nil {
			assignErr := windows.AssignProcessToJobObject(job, processHandle)
			_ = windows.CloseHandle(processHandle)
			if assignErr != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				return assignErr
			}
		}
		return command.Wait()
	}
	cleanup := func() {
		if command.ProcessState == nil {
			_ = windows.TerminateJobObject(job, 1)
		}
		_ = windows.CloseHandle(job)
	}
	return cleanup, run
}
