//go:build !linux && !darwin

package agent

import (
	"os/exec"
	"time"
)

func configureProcess(command *exec.Cmd) func() {
	command.WaitDelay = time.Second
	return func() {}
}
