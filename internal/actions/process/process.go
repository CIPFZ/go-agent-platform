// Package process starts and terminates programs on behalf of vm-agent.
//
// Two lifetimes are modelled (spec D4):
//
//   - A Tree is a command that must not outlive the service: synchronous
//     exec, background jobs and the ffmpeg recorder. The command and every
//     descendant can be terminated as one unit — a Job Object with
//     JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE on Windows, a dedicated process
//     group on Linux.
//   - Launch starts a program detached from the service (DETACHED_PROCESS
//     outside any Job on Windows, setsid on Linux) so it survives a restart.
package process

import (
	"errors"
	"os/exec"
	"sync"
)

// ErrUnsupported is returned on platforms without a process-tree primitive.
var ErrUnsupported = errors.New("process trees are not supported on this platform")

// Tree is a started command whose whole process tree can be terminated.
type Tree struct {
	cmd      *exec.Cmd
	sys      treeSys
	mu       sync.Mutex // guards sys and done against concurrent Kill
	done     bool
	waitOnce sync.Once
	waitErr  error
}

// StartTree configures cmd so its descendants can be terminated as a unit and
// starts it. cmd must not have been started.
func StartTree(cmd *exec.Cmd) (*Tree, error) {
	tree := &Tree{cmd: cmd}
	if err := tree.start(); err != nil {
		return nil, err
	}
	return tree, nil
}

// Pid returns the pid of the tree's root process.
func (t *Tree) Pid() int { return t.cmd.Process.Pid }

// Kill terminates the root process and every descendant still in the tree.
// After Wait has returned it is a no-op, so a stale pid or process group id is
// never signalled.
func (t *Tree) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil
	}
	return t.kill()
}

// Wait waits for the root process to exit, then terminates any descendants
// left in the tree and releases its resources so nothing becomes an orphan.
// It is safe to call more than once.
func (t *Tree) Wait() error {
	t.waitOnce.Do(func() {
		t.waitErr = t.cmd.Wait()
		t.mu.Lock()
		_ = t.kill()
		t.release()
		t.done = true
		t.mu.Unlock()
	})
	return t.waitErr
}
