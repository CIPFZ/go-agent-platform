package gateway

import (
	"errors"
	"path/filepath"
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
	if _, err := store.Ack(task.TaskID, "agent-1", task.LeaseToken); err != nil {
		t.Fatalf("ack task: %v", err)
	}
	failed, err := store.Result(task.TaskID, "agent-1", task.LeaseToken, protocol.TaskResult{Status: protocol.TaskStatusFailed, ErrorCode: "exit"})
	if err != nil || failed.Status != protocol.TaskStatusFailed || failed.Result == nil {
		t.Fatalf("failed result=%+v err=%v", failed, err)
	}
	if _, err := store.Pull("agent-2", 0); !errors.Is(err, ErrNoTask) {
		t.Fatalf("unsafe retry after acknowledged failure error=%v", err)
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
	if _, err := store.Ack(task.TaskID, "agent-1", task.LeaseToken); err != nil {
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

func TestTaskStorePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	first := NewTaskStore(TaskConfig{PersistencePath: path})
	created, err := first.Create(protocol.CreateTaskRequest{CallerID: "cms", IdempotencyKey: "persist-1", Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"true"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.PersistenceError(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenTaskStore(TaskConfig{PersistencePath: path})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := second.Get(created.TaskID)
	if err != nil || loaded.TaskID != created.TaskID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	again, err := second.Create(protocol.CreateTaskRequest{CallerID: "cms", IdempotencyKey: "persist-1", Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"true"}}}})
	if err != nil || again.TaskID != created.TaskID {
		t.Fatalf("idempotency after restart=%+v err=%v", again, err)
	}
}
