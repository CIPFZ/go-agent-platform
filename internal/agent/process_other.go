//go:build !linux && !darwin && !windows

package agent

import (
	"os/exec"
	"time"
)

func configureProcess(command *exec.Cmd) (func(), func() error) {
	command.WaitDelay = time.Second
	return func() {}, command.Run
}
