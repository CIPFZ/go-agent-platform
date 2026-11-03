//go:build legacy

package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/legacy/internal/protocol"
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

func TestHTTPAuthTokenProtectsAPI(t *testing.T) {
	h := NewServer(Config{AuthToken: "secret"}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	request.Header.Set("Authorization", "Bearer secret")
	recorder = httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid auth status=%d", recorder.Code)
	}
}

func TestHTTPAuthTokenFileRotatesWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.token")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewServer(Config{AuthTokenFile: path}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	request.Header.Set("Authorization", "Bearer first")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("initial file token status=%d", recorder.Code)
	}
	if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	request.Header.Set("Authorization", "Bearer first")
	recorder = httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("old file token status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	request.Header.Set("Authorization", "Bearer second")
	recorder = httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rotated file token status=%d", recorder.Code)
	}
}

func TestStorePersistsAgentIdentityAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	first := NewStore(Config{AgentPersistencePath: path})
	if _, err := first.Register(protocol.RegisterRequest{AgentID: "persisted", AgentVersion: "v1"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := first.Revoke("persisted"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := first.PersistenceError(); err != nil {
		t.Fatalf("persist agent state: %v", err)
	}
	second := NewStore(Config{AgentPersistencePath: path})
	agent, err := second.Get("persisted")
	if err != nil {
		t.Fatalf("load persisted agent: %v", err)
	}
	if agent.Status != protocol.AgentStatusRevoked || agent.RevokedAt == nil {
		t.Fatalf("persisted agent = %+v", agent)
	}
}
