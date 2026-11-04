//go:build linux

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestListFindsSelfAndFilters(t *testing.T) {
	procs, err := List("")
	if err != nil {
		t.Fatal(err)
	}
	var self *Info
	for i := range procs {
		if procs[i].PID == os.Getpid() {
			self = &procs[i]
		}
	}
	if self == nil || self.PPID != os.Getppid() || self.Exe == "" || self.StartedAt == nil || time.Since(*self.StartedAt) > time.Hour {
		t.Fatalf("self = %+v", self)
	}
	cmd := exec.Command("sleep", "30")
	cmd.Start()
	defer cmd.Process.Kill()
	filtered, _ := List("SLE")
	found := false
	for _, p := range filtered {
		if p.PID == cmd.Process.Pid && p.Name == "sleep" {
			found = true
		}
		if !strings.Contains(strings.ToLower(p.Name), "sle") {
			t.Fatalf("filter let %q through", p.Name)
		}
	}
	if !found {
		t.Fatal("sleep not listed")
	}
}

func spawnTree(t *testing.T, script string) (*exec.Cmd, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child")
	cmd := exec.Command("sh", "-c", strings.ReplaceAll(script, "PIDFILE", pidFile))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	var child int
	for i := 0; i < 200 && child == 0; i++ {
		data, _ := os.ReadFile(pidFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("child never started")
	}
	return cmd, child
}

func TestKillGracefulAndTree(t *testing.T) {
	cmd, child := spawnTree(t, "sleep 60 & echo $! > PIDFILE; wait")
	desc, _ := Descendants(cmd.Process.Pid)
	if len(desc) < 2 || desc[0] != cmd.Process.Pid {
		t.Fatalf("descendants = %v", desc)
	}
	res, err := Kill(context.Background(), KillRequest{PID: cmd.Process.Pid, Tree: true, Grace: 3 * time.Second}, nil)
	if err != nil || !res.Graceful || len(res.Forced) != 0 || !res.Exited {
		t.Fatalf("kill = %+v, %v", res, err)
	}
	if exists(child) {
		t.Fatal("grandchild survived a tree kill")
	}
}

func TestKillEscalatesAfterGrace(t *testing.T) {
	cmd, _ := spawnTree(t, "trap '' TERM; echo $$ > PIDFILE; while :; do sleep 0.05; done")
	start := time.Now()
	res, err := Kill(context.Background(), KillRequest{PID: cmd.Process.Pid, Grace: 300 * time.Millisecond}, nil)
	if err != nil || res.Graceful || len(res.Forced) != 1 || !res.Exited {
		t.Fatalf("kill = %+v, %v", res, err)
	}
	if took := time.Since(start); took < 300*time.Millisecond || took > 3*time.Second {
		t.Fatalf("escalation after %s", took)
	}
}

func TestKillForceAndNotTree(t *testing.T) {
	cmd, child := spawnTree(t, "sleep 60 & echo $! > PIDFILE; wait")
	defer exec.Command("kill", "-9", strconv.Itoa(child)).Run()
	res, err := Kill(context.Background(), KillRequest{PID: cmd.Process.Pid, Force: true}, nil)
	if err != nil || res.Graceful || len(res.Forced) != 1 {
		t.Fatalf("force = %+v, %v", res, err)
	}
	if !exists(child) {
		t.Fatal("tree=false killed the child too")
	}
}

func TestKillRefusals(t *testing.T) {
	if _, err := Kill(context.Background(), KillRequest{PID: 1 << 30}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing pid err = %v", err)
	}
	if _, err := Kill(context.Background(), KillRequest{PID: os.Getpid()}, map[int]bool{os.Getpid(): true}); !errors.Is(err, ErrProtected) {
		t.Fatalf("self err = %v", err)
	}
	cmd, _ := spawnTree(t, "echo $$ > PIDFILE; sleep 60")
	defer cmd.Process.Kill()
	// A tree containing a protected pid is refused as a whole.
	if _, err := Kill(context.Background(), KillRequest{PID: cmd.Process.Pid, Tree: true}, map[int]bool{cmd.Process.Pid: true}); !errors.Is(err, ErrProtected) {
		t.Fatalf("protected tree err = %v", err)
	}
}
