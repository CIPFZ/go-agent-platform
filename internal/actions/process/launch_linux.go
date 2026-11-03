//go:build linux

package process

import "syscall"

// Launch starts cmd in a new session, detached from vm-agent's process group
// and without a parent-death signal, so it survives a restart (spec D4).
// Its standard streams are /dev/null. The child is reaped in the background.
func Launch(spec Command) (int, error) {
	cmd := spec.cmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}
