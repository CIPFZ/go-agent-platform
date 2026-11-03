//go:build linux

package process

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"
)

type treeSys struct{}

// spawner starts children from one OS thread that never exits. Pdeathsig is
// delivered when the thread that forked the child exits, not the process, so
// forking from arbitrary Go threads could kill children prematurely.
var spawner = make(chan func())

func init() {
	go func() {
		runtime.LockOSThread()
		for fn := range spawner {
			fn()
		}
	}()
}

func onSpawnerThread(fn func() error) error {
	done := make(chan error, 1)
	spawner <- func() { done <- fn() }
	return <-done
}

func (t *Tree) start() error {
	if t.cmd.SysProcAttr == nil {
		t.cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A dedicated process group lets one signal reach every descendant that
	// did not deliberately start its own session. Pdeathsig covers the case
	// where vm-agent itself dies without running its shutdown path.
	t.cmd.SysProcAttr.Setpgid = true
	t.cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	if t.cmd.WaitDelay == 0 {
		t.cmd.WaitDelay = time.Second
	}
	return onSpawnerThread(t.cmd.Start)
}

func (t *Tree) kill() error {
	if t.cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (t *Tree) release() {}
