// Package supervisor implements vm-agent -supervise (spec section 4.1): a
// minimal parent that only restarts the real agent. It loads no business
// logic, recovers every panic, and forwards termination to the child by
// closing the child's stdin, which the child treats as a shutdown request.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
)

// EnvSupervised is set in the child's environment; the child then shuts
// down gracefully when its stdin reaches EOF.
const EnvSupervised = "VM_AGENT_SUPERVISED"

// Options configure Run.
type Options struct {
	Executable string
	Args       []string
	LockFile   string
	// RestartDelay, QuickExit, QuickLimit and Backoff implement "restart after
	// 5 s; after 10 consecutive exits within 60 s wait 60 s".
	RestartDelay time.Duration
	QuickExit    time.Duration
	QuickLimit   int
	Backoff      time.Duration
	// StopGrace bounds the wait for the child after forwarding termination.
	StopGrace time.Duration
	Logf      func(format string, args ...any)
}

// Defaults returns the spec 4.1 timings.
func Defaults(executable string, args []string, lockFile string) Options {
	return Options{
		Executable:   executable,
		Args:         args,
		LockFile:     lockFile,
		RestartDelay: 5 * time.Second,
		QuickExit:    60 * time.Second,
		QuickLimit:   10,
		Backoff:      60 * time.Second,
		StopGrace:    30 * time.Second,
		Logf:         log.Printf,
	}
}

// ErrAlreadyRunning means another supervisor owns the lock file.
var ErrAlreadyRunning = errors.New("another vm-agent supervisor is running")

// Run supervises the child until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if err := acquireLock(o.LockFile); err != nil {
		return err
	}
	defer os.Remove(o.LockFile)
	quick := 0
	for {
		ran, err := runOnce(ctx, o)
		if ctx.Err() != nil {
			return nil
		}
		if ran < o.QuickExit {
			quick++
		} else {
			quick = 0
		}
		delay := o.RestartDelay
		if quick >= o.QuickLimit {
			delay, quick = o.Backoff, 0
		}
		o.Logf("vm-agent child exited after %s (%v); restarting in %s", ran.Round(time.Millisecond), err, delay)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// runOnce starts one child and waits for it, recovering any panic so the
// supervisor itself never dies.
func runOnce(ctx context.Context, o Options) (ran time.Duration, err error) {
	started := time.Now()
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("supervisor panic: %v", v)
		}
		ran = time.Since(started)
	}()
	cmd := exec.Command(o.Executable, o.Args...)
	cmd.Env = append(os.Environ(), EnvSupervised+"=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		return 0, err
	case <-ctx.Done():
		// Forward termination and let the child shut down gracefully.
		_ = stdin.Close()
		select {
		case err := <-exited:
			return 0, err
		case <-time.After(o.StopGrace):
			_ = cmd.Process.Kill()
			return 0, <-exited
		}
	}
}

// acquireLock implements the lock file of spec 4.1: it holds the
// supervisor's pid; a live vm-agent process holding it means another
// instance is running.
func acquireLock(path string) error {
	if data, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid != os.Getpid() && process.IsSameProgram(pid) {
			return fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, pid)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WatchStdin calls stop when stdin reaches EOF if this process was started
// by a supervisor; otherwise it does nothing.
func WatchStdin(stop func()) {
	if os.Getenv(EnvSupervised) != "1" {
		return
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		stop()
	}()
}
