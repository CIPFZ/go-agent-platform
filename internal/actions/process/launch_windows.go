//go:build windows

package process

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const createBreakawayFromJob = 0x01000000

// Launch starts cmd as a detached process outside any Job Object
// (DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP), so it survives vm-agent
// (spec D4). CREATE_BREAKAWAY_FROM_JOB leaves a job vm-agent itself may run
// in, such as the scheduled task's; when that job forbids breakaway the
// program starts without it.
func Launch(spec Command) (int, error) {
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	start := func(flags uint32) (*exec.Cmd, error) {
		cmd := spec.cmd()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		return cmd, cmd.Start()
	}
	cmd, err := start(base | createBreakawayFromJob)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		cmd, err = start(base)
	}
	if err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}
