package supervisor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the supervised child when this variable is set.
const childMode = "VM_AGENT_SUPERVISOR_TEST_CHILD"

func TestMain(m *testing.M) {
	if dir := os.Getenv(childMode); dir != "" {
		runChild(dir)
		return
	}
	os.Exit(m.Run())
}

// runChild appends one line per start to dir/starts. The first two starts
// exit at once; later starts wait for stdin EOF (the supervisor's shutdown
// signal), record it and exit cleanly.
func runChild(dir string) {
	f, _ := os.OpenFile(filepath.Join(dir, "starts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(strings.Join(os.Args[1:], " ") + "|" + os.Getenv(EnvSupervised) + "\n")
	f.Close()
	data, _ := os.ReadFile(filepath.Join(dir, "starts"))
	if strings.Count(string(data), "\n") <= 2 {
		os.Exit(3)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = os.WriteFile(filepath.Join(dir, "graceful"), []byte("yes"), 0o600)
	os.Exit(0)
}

func testOptions(t *testing.T, dir string) Options {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(childMode, dir)
	return Options{
		Executable:   exe,
		Args:         []string{"-listen", "x"},
		LockFile:     filepath.Join(dir, "vm-agent.lock"),
		RestartDelay: 10 * time.Millisecond,
		QuickExit:    time.Minute,
		QuickLimit:   10,
		Backoff:      time.Hour,
		StopGrace:    5 * time.Second,
		Logf:         t.Logf,
	}
}

func TestRestartsChildAndForwardsShutdown(t *testing.T) {
	dir := t.TempDir()
	o := testOptions(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, "starts"))
		if strings.Count(string(data), "\n") >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child not restarted: %q", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	lock, err := os.ReadFile(o.LockFile)
	if err != nil || strings.TrimSpace(string(lock)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file = %q, %v", lock, err)
	}
	time.Sleep(50 * time.Millisecond) // let the third child reach its stdin read
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "graceful")); err != nil {
		t.Fatal("child did not observe the forwarded shutdown")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "starts"))
	if !strings.HasPrefix(string(data), "-listen x|1\n") {
		t.Fatalf("child args/env = %q", data)
	}
	if _, err := os.Stat(o.LockFile); !os.IsNotExist(err) {
		t.Fatal("lock file not removed")
	}
}

func TestBackoffAfterQuickExits(t *testing.T) {
	dir := t.TempDir()
	o := testOptions(t, dir)
	o.QuickLimit = 2
	var logs []string
	o.Logf = func(format string, args ...any) { logs = append(logs, format) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	// Two quick exits trigger the (one hour) back-off, so no third start.
	starts := func() int {
		data, _ := os.ReadFile(filepath.Join(dir, "starts"))
		return strings.Count(string(data), "\n")
	}
	deadline := time.Now().Add(10 * time.Second)
	for starts() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // a third start would follow 10 ms later
	cancel()
	<-done
	if n := starts(); n != 2 {
		t.Fatalf("starts = %d, want 2 before back-off", n)
	}
}

func TestLockHeldByLiveSupervisorRefusesStart(t *testing.T) {
	dir := t.TempDir()
	o := testOptions(t, dir)
	// A live process running the same executable: a child of this test.
	other := filepath.Join(dir, "other")
	_ = os.MkdirAll(other, 0o700)
	t.Setenv(childMode, other)
	_ = os.WriteFile(filepath.Join(other, "starts"), []byte("a\nb\n"), 0o600) // child will block on stdin
	exe, _ := os.Executable()
	r, w, _ := os.Pipe()
	defer w.Close()
	attr := &os.ProcAttr{Files: []*os.File{r, os.Stdout, os.Stderr}, Env: append(os.Environ(), childMode+"="+other)}
	p, err := os.StartProcess(exe, []string{exe}, attr)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	_ = os.WriteFile(o.LockFile, []byte(strconv.Itoa(p.Pid)), 0o600)
	if err := Run(context.Background(), o); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Run = %v, want ErrAlreadyRunning", err)
	}
	// A stale pid does not block.
	_ = os.WriteFile(o.LockFile, []byte("999999999"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, o); err != nil {
		t.Fatalf("stale lock: %v", err)
	}
}

func TestChildOutputGoesToLog(t *testing.T) {
	dir := t.TempDir()
	o := testOptions(t, dir)
	o.LogPath = filepath.Join(dir, "vm-agent.log")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, o) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(o.LogPath)
		if strings.Contains(string(data), "supervisor: vm-agent child exited") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log = %q", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}
