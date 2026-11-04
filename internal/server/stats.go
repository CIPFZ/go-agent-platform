package server

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// durationBuckets are the histogram upper bounds in seconds.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 600}

// Stats collects the counters exported by /v1/metrics (spec section 6.1).
type Stats struct {
	inFlight      atomic.Int64
	dedupHits     atomic.Int64
	dedupInFlight atomic.Int64
	busy          atomic.Int64

	mu        sync.Mutex
	requests  map[[2]string]int64 // action, code
	durations map[string]*histogram
}

type histogram struct {
	counts []int64 // per bucket, cumulative at render time
	sum    float64
	count  int64
}

func newStats() *Stats {
	return &Stats{requests: map[[2]string]int64{}, durations: map[string]*histogram{}}
}

func (s *Stats) observe(action, code string, elapsed time.Duration, dedupHit bool) {
	if dedupHit {
		s.dedupHits.Add(1)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[[2]string{action, code}]++
	h := s.durations[action]
	if h == nil {
		h = &histogram{counts: make([]int64, len(durationBuckets))}
		s.durations[action] = h
	}
	seconds := elapsed.Seconds()
	for i, bound := range durationBuckets {
		if seconds <= bound {
			h.counts[i]++
			break
		}
	}
	h.sum += seconds
	h.count++
}

// WritePrometheus renders the counters in the Prometheus text format.
// InFlight returns the number of requests currently executing.
func (s *Stats) InFlight() int64 { return s.inFlight.Load() }

func (s *Stats) WritePrometheus(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([][2]string, 0, len(s.requests))
	for k := range s.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	fmt.Fprintln(w, "# HELP vm_agent_requests_total Requests handled, by action and result code.")
	fmt.Fprintln(w, "# TYPE vm_agent_requests_total counter")
	for _, k := range keys {
		fmt.Fprintf(w, "vm_agent_requests_total{action=%q,code=%q} %d\n", k[0], k[1], s.requests[k])
	}
	fmt.Fprintln(w, "# HELP vm_agent_errors_total Requests answered with an error, by action and error code.")
	fmt.Fprintln(w, "# TYPE vm_agent_errors_total counter")
	for _, k := range keys {
		if k[1] != "ok" {
			fmt.Fprintf(w, "vm_agent_errors_total{action=%q,code=%q} %d\n", k[0], k[1], s.requests[k])
		}
	}
	actions := make([]string, 0, len(s.durations))
	for a := range s.durations {
		actions = append(actions, a)
	}
	sort.Strings(actions)
	fmt.Fprintln(w, "# HELP vm_agent_request_duration_seconds Request latency by action.")
	fmt.Fprintln(w, "# TYPE vm_agent_request_duration_seconds histogram")
	for _, a := range actions {
		h := s.durations[a]
		var cumulative int64
		for i, bound := range durationBuckets {
			cumulative += h.counts[i]
			fmt.Fprintf(w, "vm_agent_request_duration_seconds_bucket{action=%q,le=%q} %d\n", a, trimFloat(bound), cumulative)
		}
		fmt.Fprintf(w, "vm_agent_request_duration_seconds_bucket{action=%q,le=\"+Inf\"} %d\n", a, h.count)
		fmt.Fprintf(w, "vm_agent_request_duration_seconds_sum{action=%q} %g\n", a, h.sum)
		fmt.Fprintf(w, "vm_agent_request_duration_seconds_count{action=%q} %d\n", a, h.count)
	}
	gauge(w, "vm_agent_in_flight_requests", "Requests currently executing.", s.inFlight.Load())
	gauge(w, "vm_agent_goroutines", "Goroutines in the process.", int64(runtime.NumGoroutine()))
	counter(w, "vm_agent_dedup_hits_total", "Requests answered by replaying a cached response.", s.dedupHits.Load())
	counter(w, "vm_agent_dedup_in_flight_conflicts_total", "Requests refused because the same request id was still executing.", s.dedupInFlight.Load())
	counter(w, "vm_agent_busy_rejections_total", "Requests refused by the concurrency limit.", s.busy.Load())
}

func gauge(w io.Writer, name, help string, v int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", name, help, name, name, v)
}

func counter(w io.Writer, name, help string, v int64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
}

func trimFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
}
