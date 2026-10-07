package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskConflict = errors.New("task state conflict")
	ErrNoTask       = errors.New("no task available")
	ErrQueueFull    = errors.New("task queue is full")
	ErrTaskLease    = errors.New("task lease expired")
)

type TaskConfig struct {
	Now          func() time.Time
	MaxQueueSize int
	DefaultLease time.Duration
}

type TaskStore struct {
	mu           sync.Mutex
	tasks        map[string]protocol.Task
	idempotency  map[string]string
	now          func() time.Time
	maxQueueSize int
	defaultLease time.Duration
}

// TaskRepository is the persistence boundary used by the Gateway. TaskStore
// is the P1-P2 in-memory implementation; a durable database can implement the
// same operations without changing the HTTP protocol.
type TaskRepository interface {
	Create(protocol.CreateTaskRequest) (protocol.Task, error)
	Pull(string, time.Duration) (protocol.Task, error)
	Ack(string, string) (protocol.Task, error)
	Result(string, string, protocol.TaskResult) (protocol.Task, error)
	Cancel(string) (protocol.Task, error)
	Get(string) (protocol.Task, error)
	List() []protocol.Task
}

func NewTaskStore(config TaskConfig) *TaskStore {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	lease := config.DefaultLease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	return &TaskStore{tasks: make(map[string]protocol.Task), idempotency: make(map[string]string), now: now, maxQueueSize: config.MaxQueueSize, defaultLease: lease}
}

func (s *TaskStore) currentTime() time.Time { return s.now().UTC() }

func (s *TaskStore) Create(request protocol.CreateTaskRequest) (protocol.Task, error) {
	if err := request.Action.Validate(); err != nil {
		return protocol.Task{}, err
	}
	if strings.TrimSpace(request.CallerID) == "" {
		return protocol.Task{}, errors.New("caller_id is required")
	}
	key := request.CallerID + "\x00" + request.IdempotencyKey
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.IdempotencyKey != "" {
		if taskID := s.idempotency[key]; taskID != "" {
			return cloneTask(s.tasks[taskID]), nil
		}
	}
	if s.maxQueueSize > 0 && s.pendingCountLocked() >= s.maxQueueSize {
		return protocol.Task{}, ErrQueueFull
	}
	now := s.currentTime()
	maxAttempts := request.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	task := protocol.Task{TaskID: newTaskID(), CallerID: request.CallerID, IdempotencyKey: request.IdempotencyKey, AgentID: request.AgentID, Action: cloneAction(request.Action), Status: protocol.TaskStatusQueued, CreatedAt: now, TimeoutSeconds: request.TimeoutSeconds, MaxAttempts: maxAttempts}
	s.tasks[task.TaskID] = task
	if request.IdempotencyKey != "" {
		s.idempotency[key] = task.TaskID
	}
	return cloneTask(task), nil
}

func (s *TaskStore) Pull(agentID string, lease time.Duration) (protocol.Task, error) {
	if strings.TrimSpace(agentID) == "" {
		return protocol.Task{}, errors.New("agent_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.currentTime()
	s.requeueExpiredLocked(now)
	if lease <= 0 {
		lease = s.defaultLease
	}
	for id, task := range s.tasks {
		if task.Status != protocol.TaskStatusQueued || (task.AgentID != "" && task.AgentID != agentID) {
			continue
		}
		expires := now.Add(lease)
		task.AgentID = agentID
		task.Status = protocol.TaskStatusDispatched
		task.DispatchedAt = timePtr(now)
		task.LeaseExpiresAt = timePtr(expires)
		task.Attempt++
		s.tasks[id] = task
		return cloneTask(task), nil
	}
	return protocol.Task{}, ErrNoTask
}

func (s *TaskStore) Ack(taskID, agentID string) (protocol.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return protocol.Task{}, ErrTaskNotFound
	}
	now := s.currentTime()
	s.requeueExpiredLocked(now)
	task = s.tasks[taskID]
	if task.AgentID != agentID || task.Status != protocol.TaskStatusDispatched {
		return protocol.Task{}, ErrTaskConflict
	}
	task.Status = protocol.TaskStatusRunning
	task.StartedAt = timePtr(now)
	s.tasks[taskID] = task
	return cloneTask(task), nil
}

func (s *TaskStore) Result(taskID, agentID string, result protocol.TaskResult) (protocol.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return protocol.Task{}, ErrTaskNotFound
	}
	if task.AgentID != agentID || (task.Status != protocol.TaskStatusRunning && task.Status != protocol.TaskStatusDispatched) {
		return protocol.Task{}, ErrTaskConflict
	}
	if result.Status != protocol.TaskStatusSucceeded && result.Status != protocol.TaskStatusFailed {
		return protocol.Task{}, errors.New("result status must be succeeded or failed")
	}
	now := s.currentTime()
	result.FinishedAt = now
	if result.Status == protocol.TaskStatusFailed && task.Attempt < task.MaxAttempts {
		task.Status = protocol.TaskStatusQueued
		task.AgentID = ""
		task.LeaseExpiresAt = nil
		task.Result = nil
		s.tasks[taskID] = task
		return cloneTask(task), nil
	}
	task.Status = result.Status
	task.FinishedAt = timePtr(now)
	task.LeaseExpiresAt = nil
	task.Result = &result
	s.tasks[taskID] = task
	return cloneTask(task), nil
}

func (s *TaskStore) Cancel(taskID string) (protocol.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[taskID]
	if !ok {
		return protocol.Task{}, ErrTaskNotFound
	}
	if task.Status == protocol.TaskStatusSucceeded || task.Status == protocol.TaskStatusFailed || task.Status == protocol.TaskStatusCancelled || task.Status == protocol.TaskStatusExpired {
		return protocol.Task{}, ErrTaskConflict
	}
	now := s.currentTime()
	task.Status = protocol.TaskStatusCancelled
	task.FinishedAt = timePtr(now)
	task.LeaseExpiresAt = nil
	s.tasks[taskID] = task
	return cloneTask(task), nil
}

func (s *TaskStore) Get(taskID string) (protocol.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[taskID]; !ok {
		return protocol.Task{}, ErrTaskNotFound
	}
	s.requeueExpiredLocked(s.currentTime())
	return cloneTask(s.tasks[taskID]), nil
}

func (s *TaskStore) List() []protocol.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requeueExpiredLocked(s.currentTime())
	output := make([]protocol.Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		output = append(output, cloneTask(task))
	}
	sort.Slice(output, func(i, j int) bool { return output[i].CreatedAt.Before(output[j].CreatedAt) })
	return output
}

func (s *TaskStore) pendingCountLocked() int {
	count := 0
	for _, task := range s.tasks {
		if task.Status == protocol.TaskStatusQueued || task.Status == protocol.TaskStatusDispatched || task.Status == protocol.TaskStatusRunning {
			count++
		}
	}
	return count
}

func (s *TaskStore) requeueExpiredLocked(now time.Time) {
	for id, task := range s.tasks {
		if task.LeaseExpiresAt == nil || now.Before(*task.LeaseExpiresAt) || (task.Status != protocol.TaskStatusDispatched && task.Status != protocol.TaskStatusRunning) {
			continue
		}
		if task.Attempt < task.MaxAttempts {
			task.Status = protocol.TaskStatusQueued
			task.AgentID = ""
			task.LeaseExpiresAt = nil
		} else {
			task.Status = protocol.TaskStatusExpired
			task.FinishedAt = timePtr(now)
			task.LeaseExpiresAt = nil
		}
		s.tasks[id] = task
	}
}

func cloneTask(task protocol.Task) protocol.Task {
	task.Action = cloneAction(task.Action)
	if task.Result != nil {
		result := *task.Result
		result.Artifacts = append([]protocol.Artifact(nil), task.Result.Artifacts...)
		task.Result = &result
	}
	return task
}

func cloneAction(action protocol.TaskAction) protocol.TaskAction {
	if action.Command != nil {
		command := *action.Command
		command.Argv = append([]string(nil), action.Command.Argv...)
		command.Environment = cloneMap(action.Command.Environment)
		action.Command = &command
	}
	if action.File != nil {
		file := *action.File
		file.Content = append([]byte(nil), action.File.Content...)
		action.File = &file
	}
	if action.Screenshot != nil {
		screenshot := *action.Screenshot
		action.Screenshot = &screenshot
	}
	return action
}

func timePtr(value time.Time) *time.Time { return &value }

func newTaskID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "task-fallback"
	}
	return "task-" + hex.EncodeToString(buffer)
}
