//go:build linux

package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	result, err := Run(context.Background(), Spec{Argv: []string{"sh", "-c", "printf out; printf err >&2; exit 3"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 3 || string(result.Stdout) != "out" || string(result.Stderr) != "err" || result.Truncated {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunOutputLimitTruncatesEachStream(t *testing.T) {
	result, err := Run(context.Background(), Spec{Argv: []string{"printf", "abcdef"}, OutputLimit: 4})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(result.Stdout) != "abcd" || !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunStdinIsClosedAfterWrite(t *testing.T) {
	result, err := Run(context.Background(), Spec{Argv: []string{"cat"}, Stdin: []byte("piped")})
	if err != nil || string(result.Stdout) != "piped" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestRunEnvMergeAndClear(t *testing.T) {
	t.Setenv("VM_AGENT_INHERITED", "yes")
	result, err := Run(context.Background(), Spec{Argv: []string{"sh", "-c", `printf "%s/%s" "$VM_AGENT_INHERITED" "$VM_AGENT_SET"`}, Env: map[string]string{"VM_AGENT_SET": "set"}})
	if err != nil || string(result.Stdout) != "yes/set" {
		t.Fatalf("merge result = %q err = %v", result.Stdout, err)
	}
	result, err = Run(context.Background(), Spec{Argv: []string{"/bin/sh", "-c", `printf "%s/%s" "$VM_AGENT_INHERITED" "$VM_AGENT_SET"`}, Env: map[string]string{"VM_AGENT_SET": "set"}, EnvClear: true})
	if err != nil || string(result.Stdout) != "/set" {
		t.Fatalf("clear result = %q err = %v", result.Stdout, err)
	}
	if _, err := Run(context.Background(), Spec{Argv: []string{"true"}, Env: map[string]string{"A=B": "x"}}); !errors.Is(err, ErrInvalidEnv) {
		t.Fatalf("invalid env err = %v", err)
	}
}

func TestRunDefaultsCwdToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	result, err := Run(context.Background(), Spec{Argv: []string{"pwd"}})
	if err != nil || strings.TrimSpace(string(result.Stdout)) != home {
		t.Fatalf("pwd = %q err = %v", result.Stdout, err)
	}
}

func TestRunCancelKillsWholeTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Run(ctx, Spec{Argv: []string{"sh", "-c", "sleep 30 & echo $! > " + pidFile + "; wait"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
	assertGone(t, pidFile)
}

func TestRunKillsOrphanedDescendantsAfterExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// The shell exits immediately, leaving a background child holding no
	// pipes; the tree must still be cleaned up (spec D4).
	_, err := Run(context.Background(), Spec{Argv: []string{"sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $! > " + pidFile}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertGone(t, pidFile)
}

func assertGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant %d still alive", pid)
}
