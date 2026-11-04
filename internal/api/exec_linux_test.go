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

func TestBackgroundJobLifecycle(t *testing.T) {
	e := newEnv(t)
	var started protocol.ExecDetachedResult
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sh", "-c", "printf 'line1\\nline2\\n'; exit 3"}, "detach": true}).into(t, &started)
	if !strings.HasPrefix(started.JobID, "job-") {
		t.Fatalf("job id = %q", started.JobID)
	}
	var job protocol.JobResult
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.call("GET", "/v1/exec/jobs/"+started.JobID, nil).into(t, &job)
		if job.Status != "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.Status != "exited" || *job.ExitCode != 3 || job.Stdout != "line1\nline2\n" || job.FinishedAt == nil {
		t.Fatalf("job = %+v", job)
	}
	e.call("GET", "/v1/exec/jobs/"+started.JobID+"?tail_bytes=6", nil).into(t, &job)
	if job.Stdout != "line2\n" {
		t.Fatalf("tail = %q", job.Stdout)
	}
	var list struct {
		Jobs []map[string]any `json:"jobs"`
	}
	e.call("GET", "/v1/exec/jobs", nil).into(t, &list)
	if len(list.Jobs) != 1 || list.Jobs[0]["job_id"] != started.JobID {
		t.Fatalf("list = %v", list.Jobs)
	}
	e.call("DELETE", "/v1/exec/jobs/"+started.JobID, nil).into(t, &map[string]any{})
	e.call("GET", "/v1/exec/jobs/"+started.JobID, nil).code(t, protocol.CodeNotFound)
	e.call("GET", "/v1/exec/jobs/x?tail_bytes=-1", nil).code(t, protocol.CodeInvalidRequest)
}

func TestBackgroundJobCancelTimeoutAndShutdown(t *testing.T) {
	e := newEnv(t)
	var a, b, c protocol.ExecDetachedResult
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sleep", "60"}, "detach": true}).into(t, &a)
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sleep", "60"}, "detach": true, "job_timeout_ms": 100}).into(t, &b)
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sleep", "60"}, "detach": true}).into(t, &c)
	r := e.call("DELETE", "/v1/exec/jobs/"+a.JobID, nil)
	r.code(t, protocol.CodeConflict)
	var cancelled map[string]any
	e.call("POST", "/v1/exec/jobs/"+a.JobID+"/cancel", nil).into(t, &cancelled)
	if cancelled["status"] != "killed" || cancelled["reason"] != "cancelled" {
		t.Fatalf("cancel = %v", cancelled)
	}
	time.Sleep(500 * time.Millisecond)
	var job protocol.JobResult
	e.call("GET", "/v1/exec/jobs/"+b.JobID, nil).into(t, &job)
	if job.Status != "killed" || job.Reason != "job_timeout" {
		t.Fatalf("timed out job = %+v", job)
	}
	e.api.Shutdown()
	e.call("GET", "/v1/exec/jobs/"+c.JobID, nil).into(t, &job)
	if job.Status != "killed" || job.Reason != "agent_shutdown" {
		t.Fatalf("after shutdown = %+v", job)
	}
	e.call("POST", "/v1/exec/jobs/nope/cancel", nil).code(t, protocol.CodeNotFound)
}

func TestJobsSurviveRestartAsKilled(t *testing.T) {
	jobDir := t.TempDir()
	e := newEnv(t, func(d *Deps) { d.Config.JobDir = jobDir })
	var started protocol.ExecDetachedResult
	e.call("POST", "/v1/exec", map[string]any{"argv": []string{"sleep", "60"}, "detach": true}).into(t, &started)
	// A second API over the same directory sees the running record of a
	// process it does not own, as after a crash.
	e2 := newEnv(t, func(d *Deps) { d.Config.JobDir = jobDir })
	var job protocol.JobResult
	e2.call("GET", "/v1/exec/jobs/"+started.JobID, nil).into(t, &job)
	if job.Status != "killed" || job.Reason != "agent_restart" {
		t.Fatalf("restarted view = %+v", job)
	}
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
