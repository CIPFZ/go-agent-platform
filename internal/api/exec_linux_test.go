//go:build linux

package api

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/policy"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestExecSyncResult(t *testing.T) {
	e := newEnv(t)
	var res protocol.ExecResult
	stdin := base64.StdEncoding.EncodeToString([]byte("in"))
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sh", "-c", `cat; printf err >&2; printf '\377'; exit 7`}, "stdin": stdin}).into(t, &res)
	if res.ExitCode != 7 || res.Stdout != "in�" || res.Stderr != "err" || res.Truncated || res.DurationMS < 0 {
		t.Fatalf("result = %+v", res)
	}
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sh", "-c", "head -c 100 /dev/zero"}, "output_limit_bytes": 10}).into(t, &res)
	if len(res.Stdout) != 10 || !res.Truncated {
		t.Fatalf("limited = %+v", res)
	}
	var cwd protocol.ExecResult
	dir := t.TempDir()
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"pwd"}, "cwd": dir, "env": map[string]string{"A": "1"}}).into(t, &cwd)
	if strings.TrimSpace(cwd.Stdout) != dir {
		t.Fatalf("cwd = %q", cwd.Stdout)
	}
	ev := e.auditEvents(3) // three exec calls above
	last := ev[2]
	if last.Params["argv0"] != "pwd" || last.Params["argc"] != "1" || last.Params["env_keys"] != "A" || len(last.Params["argv_sha256"]) != 64 || last.Params["argv"] != "" {
		t.Fatalf("audit = %v", last.Params)
	}
}

func TestExecTimeoutKillsTree(t *testing.T) {
	e := newEnv(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	// The timeout leaves the shell ample time to record its child first.
	r := e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sh", "-c", "sleep 60 & echo $! >" + pidFile + "; wait"}}, protocol.HeaderTimeoutMS, "1500")
	r.code(t, protocol.CodeTimeout)
	if r.Error.Retryable || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout = %+v after %s", r.Error, time.Since(start))
	}
	data, _ := os.ReadFile(pidFile)
	pid := atoi(strings.TrimSpace(string(data)))
	if pid <= 0 {
		t.Fatalf("pid file = %q", data) // kill(0) would signal our own group
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("grandchild %d survived the timeout", pid)
	}
}

func TestExecErrors(t *testing.T) {
	deny := filepath.Join(t.TempDir(), "deny")
	os.WriteFile(deny, []byte("rm\n"), 0o600)
	pol, _ := policy.Load("", deny)
	e := newEnv(t, func(d *Deps) { d.Policy = pol })
	r := e.call("POST", "/v1/exec", map[string]any{"argv": []string{"rm", "-rf", "/nothing"}})
	r.code(t, protocol.CodeInvalidRequest)
	if r.Error.Details["reason"] != protocol.ReasonPolicyDenied {
		t.Fatalf("policy = %+v", r.Error)
	}
	e.call("POST", "/v1/process/launch", map[string]any{"argv": []string{"/bin/rm"}}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{}}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"no-such-program-xyz"}}).code(t, protocol.CodeNotFound)
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"true"}, "stdin": "%%%"}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/exec", `{"command":"ls -l"}`).code(t, protocol.CodeInvalidRequest)
}

func TestLaunchDetachesFromAgent(t *testing.T) {
	e := newEnv(t)
	var res protocol.LaunchResult
	e.call("POST", "/v1/process/launch", map[string]any{"argv": []string{"sleep", "30"}}).into(t, &res)
	if res.PID <= 0 {
		t.Fatalf("pid = %d", res.PID)
	}
	defer syscall.Kill(res.PID, syscall.SIGKILL)
	pgid, _ := syscall.Getpgid(res.PID)
	if pgid != res.PID || pgid == syscall.Getpgrp() {
		t.Fatalf("launched process group %d is not its own session", pgid)
	}
}

func TestOpenValidation(t *testing.T) {
	e := newEnv(t)
	r := e.call("POST", "/v1/process/open", map[string]any{"url": "javascript:alert(1)"})
	r.code(t, protocol.CodeInvalidRequest)
	if r.Error.Details["reason"] != protocol.ReasonURLScheme {
		t.Fatalf("scheme = %+v", r.Error)
	}
	e.call("POST", "/v1/process/open", map[string]any{"path": "relative.txt"}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/process/open", map[string]any{"path": "/a", "url": "http://x"}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/process/open", map[string]any{"path": "/definitely/missing.txt"}).code(t, protocol.CodeNotFound)
}

func TestExecDetachUnsupportedInPhase1(t *testing.T) {
	e := newEnv(t)
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"true"}, "detach": true}).code(t, protocol.CodeUnsupported)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestProcessListAndKill(t *testing.T) {
	e := newEnv(t)
	var launched protocol.LaunchResult
	e.call("POST", "/v1/process/launch", map[string]any{"argv": []string{"sleep", "120"}}).into(t, &launched)
	var list struct {
		Processes []map[string]any `json:"processes"`
	}
	e.call("GET", "/v1/process/list?name=SLEEP", nil).into(t, &list)
	found := false
	for _, p := range list.Processes {
		if int(p["pid"].(float64)) == launched.PID && p["exe"] != "" && p["started_at"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("launched sleep %d not listed: %v", launched.PID, list.Processes)
	}
	var res map[string]any
	e.call("POST", "/v1/process/kill", map[string]any{"pid": launched.PID, "grace_ms": 1000}).into(t, &res)
	if res["graceful"] != true || res["exited"] != true {
		t.Fatalf("kill = %v", res)
	}
	e.call("POST", "/v1/process/kill", map[string]any{"pid": launched.PID}).code(t, protocol.CodeNotFound)
	e.call("POST", "/v1/process/kill", map[string]any{"pid": os.Getpid()}).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/process/kill", map[string]any{"pid": 0}).code(t, protocol.CodeInvalidRequest)
}
