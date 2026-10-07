package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/gateway"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestLoadOrCreateIDPersistsIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "agent-id")
	first, err := LoadOrCreateID(path)
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	second, err := LoadOrCreateID(path)
	if err != nil {
		t.Fatalf("reload identity: %v", err)
	}
	if first == "" || first != second || len(first) != len("agent-")+32 {
		t.Fatalf("identity was not persisted: first=%q second=%q", first, second)
	}
}

func TestRunnerRegistersHeartbeatsAndGracefullyGoesOffline(t *testing.T) {
	gatewayServer := gateway.NewServer(gateway.Config{HeartbeatTimeout: time.Second})
	server := httptest.NewServer(gatewayServer.Handler())
	defer server.Close()

	client, err := NewClient(Config{
		GatewayURL:        server.URL,
		AgentID:           "agent-runner",
		AgentVersion:      "test",
		HeartbeatInterval: 5 * time.Millisecond,
		RequestTimeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Runner{Client: client, HeartbeatInterval: 5 * time.Millisecond, ShutdownTimeout: time.Second}).Run(ctx)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runner error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop")
	}

	status, err := gatewayServer.Store().Get("agent-runner")
	if err != nil {
		t.Fatalf("read runner status: %v", err)
	}
	if status.Status != protocol.AgentStatusOffline {
		t.Fatalf("runner did not mark agent offline: %+v", status)
	}
}

func TestRunnerPullsAndExecutesTask(t *testing.T) {
	gatewayServer := gateway.NewServer(gateway.Config{HeartbeatTimeout: time.Second})
	server := httptest.NewServer(gatewayServer.Handler())
	defer server.Close()
	client, err := NewClient(Config{GatewayURL: server.URL, AgentID: "agent-executor", AgentVersion: "test", HeartbeatInterval: 5 * time.Millisecond, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request := protocol.CreateTaskRequest{Envelope: protocol.Envelope{ProtocolVersion: protocol.Version, RequestID: "create-1", Timestamp: time.Now().UTC()}, CallerID: "test", IdempotencyKey: "task-1", AgentID: "agent-executor", Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"printf", "executed"}}}}
	payload, _ := json.Marshal(request)
	response, err := http.Post(server.URL+"/api/v1/tasks", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create task request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create task status=%d", response.StatusCode)
	}
	var created protocol.TaskResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Runner{Client: client, HeartbeatInterval: 5 * time.Millisecond, Executor: &Executor{Workspace: t.TempDir()}}).Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		task, getErr := gatewayServer.Tasks().Get(created.Task.TaskID)
		if getErr == nil && task.Status == protocol.TaskStatusSucceeded {
			if task.Result == nil || task.Result.Stdout != "executed" {
				t.Fatalf("unexpected task result: %+v", task.Result)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("task did not complete: task=%+v err=%v", task, getErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runner error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after task")
	}
}

type cancellationExecutor struct {
	started chan struct{}
}

func (e cancellationExecutor) Execute(ctx context.Context, _ protocol.Task) protocol.TaskResult {
	close(e.started)
	<-ctx.Done()
	return protocol.TaskResult{Status: protocol.TaskStatusCancelled, ErrorCode: "cancelled"}
}

func TestRunnerCancelsRunningTaskAndReleasesWorker(t *testing.T) {
	gatewayServer := gateway.NewServer(gateway.Config{HeartbeatTimeout: time.Second})
	server := httptest.NewServer(gatewayServer.Handler())
	defer server.Close()
	client, err := NewClient(Config{GatewayURL: server.URL, AgentID: "agent-cancel", AgentVersion: "test", HeartbeatInterval: 5 * time.Millisecond, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	task, err := gatewayServer.Tasks().Create(protocol.CreateTaskRequest{
		CallerID: "test", IdempotencyKey: "cancel-1", AgentID: "agent-cancel",
		Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"printf", "ignored"}}},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	executor := cancellationExecutor{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Runner{Client: client, HeartbeatInterval: 5 * time.Millisecond, TaskPollInterval: 5 * time.Millisecond, LeaseSeconds: 1, Executor: executor}).Run(ctx)
	}()
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("executor did not start")
	}
	if _, err := gatewayServer.Tasks().Cancel(task.TaskID); err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, getErr := gatewayServer.Tasks().Get(task.TaskID)
		if getErr == nil && current.Status == protocol.TaskStatusCancelled {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("task did not cancel: task=%+v err=%v", current, getErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case runErr := <-done:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("runner error = %v, want context cancellation", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after cancellation")
	}
}
