//go:build linux

package process

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// An orphaned tree recorded by a previous run is killed by ReapOrphans,
// while records for reused pids are ignored.
func TestReapOrphans(t *testing.T) {
	dir := t.TempDir()
	// Simulate the previous run's tree: a process group we did not reap.
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	go cmd.Wait()
	start, err := startTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, strconv.Itoa(cmd.Process.Pid)), []byte(start), 0o600)
	// A record whose pid now belongs to another process (wrong start time).
	os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())), []byte("1"), 0o600)

	killed := ReapOrphans(dir)
	if len(killed) != 1 || killed[0] != cmd.Process.Pid {
		t.Fatalf("killed = %v", killed)
	}
	deadline := time.Now().Add(3 * time.Second)
	for alive(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("records left: %v", entries)
	}
	if !alive(os.Getpid()) {
		t.Fatal("unreachable")
	}
}

func TestTreesAreRegisteredWhileRunning(t *testing.T) {
	dir := t.TempDir()
	if err := TrackTrees(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { registryDir = "" }()
	tree, err := StartTree(exec.Command("sleep", "0.2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(tree.Pid()))); err != nil {
		t.Fatalf("running tree not registered: %v", err)
	}
	var exitErr *exec.ExitError
	if err := tree.Wait(); err != nil && !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("record not removed: %v", entries)
	}
}
