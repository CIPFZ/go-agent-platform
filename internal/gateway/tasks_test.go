package gateway

import (
	"errors"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestTaskStoreIdempotencyLeaseAndResult(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	store := NewTaskStore(TaskConfig{Now: clock.Now, DefaultLease: time.Minute})
	request := protocol.CreateTaskRequest{CallerID: "cms", IdempotencyKey: "key-1", Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"printf", "ok"}}}, MaxAttempts: 2}
	first, err := store.Create(request)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	second, err := store.Create(request)
	if err != nil || first.TaskID != second.TaskID {
		t.Fatalf("idempotent create first=%+v second=%+v err=%v", first, second, err)
	}
	task, err := store.Pull("agent-1", 0)
	if err != nil || task.Status != protocol.TaskStatusDispatched || task.Attempt != 1 {
		t.Fatalf("pull task=%+v err=%v", task, err)
	}
	if _, err := store.Ack(task.TaskID, "agent-1"); err != nil {
		t.Fatalf("ack task: %v", err)
	}
	failed, err := store.Result(task.TaskID, "agent-1", protocol.TaskResult{Status: protocol.TaskStatusFailed, ErrorCode: "exit"})
	if err != nil || failed.Status != protocol.TaskStatusQueued {
		t.Fatalf("retry result=%+v err=%v", failed, err)
	}
	clock.now = clock.now.Add(time.Minute)
	task, err = store.Pull("agent-2", 0)
	if err != nil || task.Attempt != 2 || task.AgentID != "agent-2" {
		t.Fatalf("second pull task=%+v err=%v", task, err)
	}
	if _, err := store.Ack(task.TaskID, "agent-2"); err != nil {
		t.Fatalf("second ack: %v", err)
	}
	completed, err := store.Result(task.TaskID, "agent-2", protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Stdout: "ok"})
	if err != nil || completed.Status != protocol.TaskStatusSucceeded || completed.Result == nil {
		t.Fatalf("completed task=%+v err=%v", completed, err)
	}
	if _, err := store.Pull("agent-3", 0); !errors.Is(err, ErrNoTask) {
		t.Fatalf("pull after completion error=%v", err)
	}
}

func TestTaskStoreLeaseExpires(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	store := NewTaskStore(TaskConfig{Now: clock.Now, DefaultLease: time.Second})
	task, err := store.Create(protocol.CreateTaskRequest{CallerID: "cms", Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"true"}}}, MaxAttempts: 1})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.Pull("agent-1", time.Second); err != nil {
		t.Fatalf("pull task: %v", err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	expired, err := store.Get(task.TaskID)
	if err != nil || expired.Status != protocol.TaskStatusExpired {
		t.Fatalf("expired task=%+v err=%v", expired, err)
	}
}

func TestTaskStoreExecutionTimeout(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	store := NewTaskStore(TaskConfig{Now: clock.Now, DefaultLease: time.Minute})
	task, err := store.Create(protocol.CreateTaskRequest{CallerID: "cms", TimeoutSeconds: 2, Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"sleep", "10"}}}})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	task, err = store.Pull("agent-1", time.Minute)
	if err != nil {
		t.Fatalf("pull task: %v", err)
	}
	if _, err := store.Ack(task.TaskID, "agent-1"); err != nil {
		t.Fatalf("ack task: %v", err)
	}
	clock.now = clock.now.Add(3 * time.Second)
	timedOut, err := store.Get(task.TaskID)
	if err != nil || timedOut.Status != protocol.TaskStatusExpired {
		t.Fatalf("timed out task=%+v err=%v", timedOut, err)
	}
}

func BenchmarkTaskStorePull(b *testing.B) {
	store := NewTaskStore(TaskConfig{MaxQueueSize: b.N + 1})
	for i := 0; i < b.N; i++ {
		_, err := store.Create(protocol.CreateTaskRequest{CallerID: "bench", IdempotencyKey: string(rune(i + 1)), Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"true"}}}})
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Pull("agent-bench", 0); err != nil {
			b.Fatal(err)
		}
	}
}
