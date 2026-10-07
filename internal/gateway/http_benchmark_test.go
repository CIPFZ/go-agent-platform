package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func BenchmarkHTTPHealthz(b *testing.B) {
	server := httptest.NewServer(NewServer(Config{}).Handler())
	defer server.Close()
	client := server.Client()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			response, err := client.Get(server.URL + "/healthz")
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				b.Fatalf("health status=%d", response.StatusCode)
			}
		}
	})
}

func BenchmarkHTTPTaskCreate(b *testing.B) {
	gateway := NewServer(Config{})
	server := httptest.NewServer(gateway.Handler())
	defer server.Close()
	// This benchmark measures HTTP/task creation cost. Deliberately size the
	// in-memory queue for the benchmark's requested iteration count so the
	// production default queue limit is not mistaken for an HTTP failure.
	gateway.tasks.mu.Lock()
	gateway.tasks.maxQueueSize = b.N + 1
	gateway.tasks.maxCallerQueue = b.N + 1
	gateway.tasks.maxAgentQueue = b.N + 1
	gateway.tasks.mu.Unlock()
	client := server.Client()
	var sequence atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := sequence.Add(1)
			key := "bench-" + strconv.FormatUint(id, 10)
			request := protocol.CreateTaskRequest{
				Envelope: protocol.Envelope{ProtocolVersion: protocol.Version, RequestID: key, Timestamp: time.Now().UTC()},
				CallerID: "benchmark", IdempotencyKey: key,
				Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"true"}}},
			}
			payload, err := json.Marshal(request)
			if err != nil {
				b.Fatal(err)
			}
			response, err := client.Post(server.URL+"/api/v1/tasks", "application/json", bytes.NewReader(payload))
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusCreated {
				b.Fatalf("create status=%d", response.StatusCode)
			}
		}
	})
}
