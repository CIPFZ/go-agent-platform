package api

import (
	"strings"
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestMetricsExposition(t *testing.T) {
	e := newEnv(t)
	e.call("GET", "/v1/probe", nil)
	e.call("POST", "/v1/file/read", `{"path":"relative"}`)
	e.call("POST", "/v1/file/mkdir", map[string]any{"path": t.TempDir() + "/m"}, protocol.HeaderRequestID, "metrics-dedup-000001")
	e.call("POST", "/v1/file/mkdir", map[string]any{"path": t.TempDir() + "/m"}, protocol.HeaderRequestID, "metrics-dedup-000001")
	e.lastAudit("file.mkdir")
	r := e.call("GET", "/v1/metrics", nil)
	body := string(r.Body)
	if r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d content type %q", r.Status, r.Header.Get("Content-Type"))
	}
	for _, want := range []string{
		`vm_agent_requests_total{action="probe",code="ok"} 1`,
		`vm_agent_errors_total{action="file.read",code="invalid_request"} 1`,
		`vm_agent_request_duration_seconds_bucket{action="probe",le="+Inf"} 1`,
		`vm_agent_request_duration_seconds_count{action="probe"} 1`,
		"vm_agent_in_flight_requests 1",
		"vm_agent_dedup_hits_total 1",
		"vm_agent_dedup_in_flight_conflicts_total 0",
		"vm_agent_busy_rejections_total 0",
		"# TYPE vm_agent_goroutines gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q\n%s", want, body)
		}
	}
}
