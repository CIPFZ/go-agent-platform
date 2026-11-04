package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

// A synchronous exec still running when the shutdown grace period ends is
// cancelled (spec 4.4): the caller gets a retryable timeout with
// details.reason=agent_shutdown, and the request's audit record exists by
// the time GracefulStop returns, before the shutdown record.
func TestShutdownAbortsRunningExec(t *testing.T) {
	e := newEnv(t)
	argv := []string{"sleep", "30"}
	if runtime.GOOS == "windows" {
		argv = []string{"ping", "-n", "30", "127.0.0.1"}
	}
	body, _ := json.Marshal(map[string]any{"argv": argv})
	type answer struct {
		status int
		data   []byte
		err    error
	}
	answered := make(chan answer, 1)
	go func() {
		resp, err := http.Post(e.http.URL+"/v1/exec", "application/json", bytes.NewReader(body))
		if err != nil {
			answered <- answer{err: err}
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		answered <- answer{status: resp.StatusCode, data: data}
	}()
	// Wait until the exec is executing.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if e.srv.Stats().InFlight() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exec did not start")
		}
	}
	start := time.Now()
	aborted, unfinished := server.GracefulStop(e.http.Config, e.srv, 300*time.Millisecond, 2*time.Second)
	if aborted != 1 || unfinished != 0 {
		t.Fatalf("aborted %d, unfinished %d", aborted, unfinished)
	}
	if took := time.Since(start); took > 2300*time.Millisecond {
		t.Fatalf("GracefulStop took %s", took)
	}
	code := "none"
	for _, ev := range e.audit.List() {
		if ev.Action == "exec" {
			code = ev.Code
		}
	}
	if code != string(protocol.CodeTimeout) {
		t.Fatalf("exec audit record when GracefulStop returned: %s", code)
	}
	a := <-answered
	if a.err != nil {
		t.Fatal(a.err)
	}
	var resp protocol.Response
	if err := json.Unmarshal(a.data, &resp); err != nil {
		t.Fatalf("decode %s: %v", a.data, err)
	}
	if a.status != http.StatusGatewayTimeout || resp.Error == nil || resp.Error.Code != protocol.CodeTimeout ||
		!resp.Error.Retryable || resp.Error.Details["reason"] != protocol.ReasonAgentShutdown {
		t.Fatalf("status %d body %s", a.status, a.data)
	}
}
