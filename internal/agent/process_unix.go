//go:build linux || darwin

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Each task has its own process group. Cancellation also stops descendants
// which inherited stdout/stderr, so they cannot keep Wait blocked indefinitely.
func configureProcess(command *exec.Cmd) func() {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = time.Second
	kill := func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Cancel = kill
	return func() {
		if command.ProcessState == nil {
			_ = kill()
		}
	}
}
