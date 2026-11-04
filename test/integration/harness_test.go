//go:build linux

// Package integration runs vm-agent against a real X server. The tests are
// skipped unless VM_AGENT_INTEGRATION=1, because they move the pointer and
// type into whatever session $DISPLAY points at; CI runs them on Xvfb with
// openbox (spec section 12).
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

var (
	binDir    string
	agentBin  string
	xappBin   string
	enabled   = os.Getenv("VM_AGENT_INTEGRATION") == "1"
	buildOnce sync.Once
	buildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

func requireX(t *testing.T) {
	t.Helper()
	if !enabled {
		t.Skip("set VM_AGENT_INTEGRATION=1 to run against $DISPLAY")
	}
	if os.Getenv("DISPLAY") == "" {
		t.Fatal("VM_AGENT_INTEGRATION=1 requires DISPLAY")
	}
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "vm-agent-it-")
		if buildErr != nil {
			return
		}
		agentBin = filepath.Join(binDir, "vm-agent")
		xappBin = filepath.Join(binDir, "xapp")
		for target, pkg := range map[string]string{agentBin: "../../cmd/vm-agent", xappBin: "../xapp"} {
			args := []string{"build", "-o", target, pkg}
			if os.Getenv("VM_AGENT_RACE") == "1" {
				args = append([]string{"build", "-race"}, args[1:]...)
			}
			out, err := exec.Command("go", args...).CombinedOutput()
			if err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", pkg, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// agent is one running vm-agent.
type agent struct {
	t      *testing.T
	cmd    *exec.Cmd
	addr   string
	dir    string
	audit  string
	env    []string
	args   []string
	client *client.Client
	exited chan struct{}
}

type agentOption func(*agent)

func withEnv(kv ...string) agentOption { return func(a *agent) { a.env = append(a.env, kv...) } }
func withArgs(args ...string) agentOption {
	return func(a *agent) { a.args = append(a.args, args...) }
}

func startAgent(t *testing.T, opts ...agentOption) *agent {
	t.Helper()
	dir := t.TempDir()
	a := &agent{t: t, dir: dir, addr: freePort(t), audit: filepath.Join(dir, "audit", "audit.jsonl")}
	for _, o := range opts {
		o(a)
	}
	a.start()
	t.Cleanup(a.stop)
	return a
}

func (a *agent) start() {
	a.t.Helper()
	args := append([]string{"-listen", a.addr, "-audit-log", a.audit, "-job-dir", filepath.Join(a.dir, "jobs"), "-record-dir", filepath.Join(a.dir, "records")}, a.args...)
	a.cmd = exec.Command(agentBin, args...)
	a.cmd.Env = append(os.Environ(), a.env...)
	logf, _ := os.Create(filepath.Join(a.dir, "agent.log"))
	a.cmd.Stdout, a.cmd.Stderr = logf, logf
	if err := a.cmd.Start(); err != nil {
		a.t.Fatal(err)
	}
	a.exited = make(chan struct{})
	go func() { a.cmd.Wait(); logf.Close(); close(a.exited) }()
	a.client = client.New("http://" + a.addr)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := a.client.Probe(context.Background()); err == nil {
			return
		}
		select {
		case <-a.exited:
			log, _ := os.ReadFile(filepath.Join(a.dir, "agent.log"))
			a.t.Fatalf("vm-agent exited at startup:\n%s", log)
		default:
		}
		if time.Now().After(deadline) {
			a.t.Fatal("vm-agent did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop terminates gracefully (SIGTERM).
func (a *agent) stop() {
	if a.cmd == nil || a.cmd.Process == nil {
		return
	}
	select {
	case <-a.exited:
		return
	default:
	}
	a.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-a.exited:
	case <-time.After(20 * time.Second):
		a.cmd.Process.Kill()
		<-a.exited
	}
}

// crash kills vm-agent without its shutdown path.
func (a *agent) crash() {
	a.cmd.Process.Kill()
	<-a.exited
}

func (a *agent) auditEvents(t *testing.T) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(a.audit)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

// xwin is one test window.
type xwin struct {
	cmd    *exec.Cmd
	events string
	id     string
	title  string
	done   chan struct{}
}

type xwinSpec struct {
	title, class, color string
	x, y, w, h          int
	pattern             bool
	winType             string
	pid                 int
}

func startWindow(t *testing.T, s xwinSpec) *xwin {
	t.Helper()
	dir := t.TempDir()
	if s.class == "" {
		s.class = "XApp"
	}
	if s.color == "" {
		s.color = "3366cc"
	}
	if s.w == 0 {
		s.w, s.h = 400, 300
	}
	w := &xwin{events: filepath.Join(dir, "events"), title: s.title, done: make(chan struct{})}
	ready := filepath.Join(dir, "ready")
	w.cmd = exec.Command(xappBin, "-title", s.title, "-class", s.class, "-color", s.color, "-events", w.events, "-ready", ready,
		"-x", fmt.Sprint(s.x), "-y", fmt.Sprint(s.y), "-w", fmt.Sprint(s.w), "-h", fmt.Sprint(s.h), fmt.Sprintf("-pattern=%v", s.pattern), "-type", s.winType, "-pid", fmt.Sprint(s.pid))
	w.cmd.Stderr = os.Stderr
	if err := w.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { w.cmd.Wait(); close(w.done) }()
	t.Cleanup(func() {
		select {
		case <-w.done:
		default:
			w.cmd.Process.Kill()
			<-w.done
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(ready); err == nil && len(data) > 0 {
			var xid uint64
			fmt.Sscan(string(data), &xid)
			w.id = fmt.Sprintf("0x%x", xid)
			time.Sleep(200 * time.Millisecond) // let the window manager place it
			return w
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("window %q never mapped", s.title)
	return nil
}

type xevent struct {
	Type   string `json:"type"`
	Button int    `json:"button"`
	X, Y   int
	Keysym uint32 `json:"keysym"`
}

// eventsAfter waits until the window has logged at least n events (or the
// timeout) and returns them.
func (w *xwin) eventsAfter(t *testing.T, n int, timeout time.Duration) []xevent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		evs := w.readEvents(t)
		if len(evs) >= n || time.Now().After(deadline) {
			return evs
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (w *xwin) readEvents(t *testing.T) []xevent {
	f, err := os.Open(w.events)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []xevent
	s := bufio.NewScanner(f)
	for s.Scan() {
		var e xevent
		if json.Unmarshal(s.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (w *xwin) buttons(t *testing.T, n int) []xevent {
	var out []xevent
	for _, e := range w.eventsAfter(t, n, 5*time.Second) {
		if e.Type == "button" {
			out = append(out, e)
		}
	}
	return out
}
