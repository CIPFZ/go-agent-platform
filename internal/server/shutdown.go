package server

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrAgentShutdown is the cancellation cause of requests aborted by
// GracefulStop.
var ErrAgentShutdown = errors.New("vm-agent is shutting down")

// Spec 4.4: in-flight requests get ShutdownGrace to finish; those still
// running are then cancelled and get AbortWait to write their response and
// audit record.
const (
	ShutdownGrace = 10 * time.Second
	AbortWait     = 2 * time.Second
)

// GracefulStop stops hs accepting requests and waits up to grace for the
// in-flight ones (spec 4.4 steps 1 and 2). Requests still running then have
// their context cancelled with ErrAgentShutdown, so they answer
// timeout/agent_shutdown, and GracefulStop waits up to abortWait for their
// responses and audit records. It reports how many requests were aborted
// and how many of those had not finished when it returned.
func GracefulStop(hs *http.Server, s *Server, grace, abortWait time.Duration) (aborted, unfinished int64) {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	err := hs.Shutdown(ctx)
	cancel()
	if err == nil {
		return 0, 0
	}
	aborted = s.stats.inFlight.Load()
	s.abort(ErrAgentShutdown)
	deadline := time.Now().Add(abortWait)
	for s.stats.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return aborted, s.stats.inFlight.Load()
}
