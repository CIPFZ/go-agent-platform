package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/agent"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func TestStoreLifecycleAndOfflineState(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	store := NewStore(Config{HeartbeatTimeout: time.Minute, Now: clock.Now})
	registered, err := store.Register(protocol.RegisterRequest{
		AgentID:      "agent-1",
		AgentVersion: "1.0.0",
		Capabilities: []protocol.Capability{{Name: "command.exec", Attributes: map[string]string{"shell": "false"}}},
		Resource:     protocol.ResourceSummary{CPUCount: 2},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if registered.Status != protocol.AgentStatusOnline || registered.RegisteredAt != clock.now {
		t.Fatalf("unexpected registration: %+v", registered)
	}

	// Returned capabilities are copies, so callers cannot mutate store state.
	registered.Capabilities[0].Attributes["shell"] = "true"
	got, err := store.Get("agent-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Capabilities[0].Attributes["shell"] != "false" {
		t.Fatalf("store was mutated through returned capability: %+v", got.Capabilities)
	}

	clock.now = clock.now.Add(61 * time.Second)
	got, err = store.Get("agent-1")
	if err != nil || got.Status != protocol.AgentStatusOffline {
		t.Fatalf("expired heartbeat status = %+v, err=%v", got, err)
	}

	if _, err := store.Heartbeat("agent-1", protocol.HeartbeatRequest{}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	got, _ = store.Get("agent-1")
	if got.Status != protocol.AgentStatusOnline {
		t.Fatalf("heartbeat did not restore online status: %+v", got)
	}
	if _, err := store.Offline("agent-1"); err != nil {
		t.Fatalf("offline: %v", err)
	}
	got, _ = store.Get("agent-1")
	if got.Status != protocol.AgentStatusOffline {
		t.Fatalf("clean offline status was lost: %+v", got)
	}
	if _, err := store.Heartbeat("agent-1", protocol.HeartbeatRequest{}); err != nil {
		t.Fatalf("heartbeat after offline: %v", err)
	}
	got, _ = store.Get("agent-1")
	if got.Status != protocol.AgentStatusOnline {
		t.Fatalf("heartbeat did not restore cleanly offline agent: %+v", got)
	}

	if _, err := store.Revoke("agent-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := store.Heartbeat("agent-1", protocol.HeartbeatRequest{}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("heartbeat after revoke error = %v, want %v", err, ErrRevoked)
	}
}

func TestHTTPRegisterHeartbeatAndStatus(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(NewServer(Config{HeartbeatTimeout: time.Minute, Now: clock.Now}).Handler())
	defer server.Close()

	client, err := agent.NewClient(agent.Config{
		GatewayURL:   server.URL,
		AgentID:      "agent-http",
		AgentVersion: "1.2.3",
		Now:          clock.Now,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	registered, err := client.Register(context.Background())
	if err != nil {
		t.Fatalf("HTTP register: %v", err)
	}
	if registered.Status != protocol.AgentStatusOnline {
		t.Fatalf("HTTP register status = %s", registered.Status)
	}
	clock.now = clock.now.Add(30 * time.Second)
	if _, err := client.Heartbeat(context.Background()); err != nil {
		t.Fatalf("HTTP heartbeat: %v", err)
	}

	response, err := http.Get(server.URL + "/api/v1/agents/agent-http")
	if err != nil {
		t.Fatalf("HTTP get: %v", err)
	}
	defer response.Body.Close()
	var body protocol.AgentResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode HTTP get: %v", err)
	}
	if response.StatusCode != http.StatusOK || body.Agent.Status != protocol.AgentStatusOnline {
		t.Fatalf("HTTP get status=%d agent=%+v", response.StatusCode, body.Agent)
	}

	clock.now = clock.now.Add(2 * time.Minute)
	response, err = http.Get(server.URL + "/api/v1/agents/agent-http")
	if err != nil {
		t.Fatalf("HTTP get offline: %v", err)
	}
	defer response.Body.Close()
	body = protocol.AgentResponse{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode HTTP offline get: %v", err)
	}
	if body.Agent.Status != protocol.AgentStatusOffline {
		t.Fatalf("HTTP timeout status = %s", body.Agent.Status)
	}

	bad := httptest.NewRequest(http.MethodPost, server.URL+"/api/v1/agents/register", nil)
	badRecorder := httptest.NewRecorder()
	NewServer(Config{Now: clock.Now}).ServeHTTP(badRecorder, bad)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid register status = %d", badRecorder.Code)
	}
}
