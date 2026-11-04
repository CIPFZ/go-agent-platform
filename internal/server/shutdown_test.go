package server

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// An aborted request keeps the details of its own timeout (input/batch's
// executed count), becomes retryable and says agent_shutdown; a request that
// finishes within the grace period is not aborted.
func TestGracefulStopAbortsLateRequests(t *testing.T) {
	h := newHarness(t, 4, &Route{Method: "POST", Path: "/v1/late", Action: "late", Class: NonRetryable, Handler: func(c *Call) (any, error) {
		<-c.Context(time.Minute).Done()
		return nil, protocol.Errorf(protocol.CodeTimeout, "batch cut short").WithDetail("executed", 3)
	}})
	got := make(chan envelope, 1)
	go func() {
		resp, err := http.Post(h.http.URL+"/v1/late", "application/json", nil)
		if err != nil {
			got <- envelope{}
			return
		}
		defer resp.Body.Close()
		var env envelope
		data, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(data, &env)
		got <- env
	}()
	for deadline := time.Now().Add(5 * time.Second); h.srv.Stats().InFlight() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("request did not start")
		}
	}
	if aborted, unfinished := GracefulStop(h.http.Config, h.srv, 100*time.Millisecond, 2*time.Second); aborted != 1 || unfinished != 0 {
		t.Fatalf("aborted %d unfinished %d", aborted, unfinished)
	}
	env := <-got
	if env.Error == nil || env.Error.Code != protocol.CodeTimeout || !env.Error.Retryable ||
		env.Error.Details["reason"] != protocol.ReasonAgentShutdown || env.Error.Details["executed"] != float64(3) {
		t.Fatalf("aborted response %+v", env.Error)
	}
	if ev := h.events(t, 1); ev[len(ev)-1].Action != "late" || ev[len(ev)-1].Code != "timeout" {
		t.Fatalf("audit %+v", ev[len(ev)-1])
	}

	idle := newHarness(t, 4)
	if aborted, _ := GracefulStop(idle.http.Config, idle.srv, time.Second, time.Second); aborted != 0 {
		t.Fatalf("idle server aborted %d", aborted)
	}
}
