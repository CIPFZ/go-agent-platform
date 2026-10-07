package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

var (
	ErrTaskNotFound        = errors.New("task not found")
	ErrTaskConflict        = errors.New("task state conflict")
	ErrNoTask              = errors.New("no task available")
	ErrQueueFull           = errors.New("task queue is full")
	ErrTaskLease           = errors.New("task lease expired or superseded")
	ErrTaskInvalid         = errors.New("invalid task request")
	ErrIdempotencyConflict = errors.New("idempotency key was used for different task parameters")
)

type TaskConfig struct {
	Now             func() time.Time
	MaxQueueSize    int
	MaxCallerQueue  int
	MaxAgentQueue   int
	PersistencePath string
	DefaultLease    time.Duration
}

type TaskStore struct {
	mu                                          sync.Mutex
	tasks                                       map[string]protocol.Task
	idempotency                                 map[string]string
	requests                                    map[string]protocol.CreateTaskRequest
	order                                       []string
	now                                         func() time.Time
	maxQueueSize, maxCallerQueue, maxAgentQueue int
	defaultLease                                time.Duration
	persistencePath                             string
	lastPersistenceErr                          error
}

func NewTaskStore(config TaskConfig) *TaskStore {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.DefaultLease <= 0 {
		config.DefaultLease = 30 * time.Second
	}
	if config.MaxQueueSize <= 0 {
		config.MaxQueueSize = 10000
	}
	if config.MaxCallerQueue <= 0 {
		config.MaxCallerQueue = config.MaxQueueSize
	}
	if config.MaxAgentQueue <= 0 {
		config.MaxAgentQueue = config.MaxQueueSize
	}
	store := &TaskStore{tasks: make(map[string]protocol.Task), idempotency: make(map[string]string), requests: make(map[string]protocol.CreateTaskRequest), now: config.Now, defaultLease: config.DefaultLease, persistencePath: config.PersistencePath, maxQueueSize: config.MaxQueueSize, maxCallerQueue: config.MaxCallerQueue, maxAgentQueue: config.MaxAgentQueue}
	if config.PersistencePath != "" {
		if err := store.load(); err != nil {
			store.lastPersistenceErr = err
		}
	}
	return store
}
func (s *TaskStore) currentTime() time.Time { return s.now().UTC() }

func (s *TaskStore) Create(request protocol.CreateTaskRequest) (protocol.Task, error) {
	if err := request.Action.Validate(); err != nil {
		return protocol.Task{}, fmt.Errorf("%w: %v", ErrTaskInvalid, err)
	}
	if strings.TrimSpace(request.CallerID) == "" || strings.ContainsRune(request.CallerID, 0) {
		return protocol.Task{}, fmt.Errorf("%w: caller_id is required", ErrTaskInvalid)
	}
	if request.MaxAttempts < 0 || request.MaxAttempts > 10 || request.TimeoutSeconds < 0 || request.TimeoutSeconds > 86400 {
		return protocol.Task{}, fmt.Errorf("%w: max_attempts must be 0..10 and timeout_seconds 0..86400", ErrTaskInvalid)
	}
	if request.MaxAttempts == 0 {
		request.MaxAttempts = 3
	}
	if request.TimeoutSeconds == 0 {
		request.TimeoutSeconds = 30
	}
	request.Envelope = protocol.Envelope{}
	request.Action = cloneAction(request.Action)
	key := request.CallerID + "\x00" + request.IdempotencyKey
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	s.expireLocked(s.currentTime())
	if request.IdempotencyKey != "" {
		if id, ok := s.idempotency[key]; ok {
			if !reflect.DeepEqual(s.requests[id], request) {
				return protocol.Task{}, ErrIdempotencyConflict
			}
			return cloneTask(s.tasks[id]), nil
		}
	}
	total, caller, agent := 0, 0, 0
	for _, t := range s.tasks {
		if terminal(t.Status) {
			continue
		}
		total++
		if t.CallerID == request.CallerID {
			caller++
		}
		if t.TargetAgentID == request.AgentID {
			agent++
		}
	}
	if total >= s.maxQueueSize || caller >= s.maxCallerQueue || (request.AgentID != "" && agent >= s.maxAgentQueue) {
		return protocol.Task{}, ErrQueueFull
	}
	id, err := randomID("task-")
	if err != nil {
		return protocol.Task{}, err
	}
	task := protocol.Task{TaskID: id, CallerID: request.CallerID, IdempotencyKey: request.IdempotencyKey, TargetAgentID: request.AgentID, AgentID: request.AgentID, Action: cloneAction(request.Action), Status: protocol.TaskStatusQueued, CreatedAt: s.currentTime(), TimeoutSeconds: request.TimeoutSeconds, MaxAttempts: request.MaxAttempts}
	s.tasks[id] = task
	s.requests[id] = request
	s.order = append(s.order, id)
	if request.IdempotencyKey != "" {
		s.idempotency[key] = id
	}
	return cloneTask(task), nil
}

func (s *TaskStore) Pull(agentID string, lease time.Duration) (protocol.Task, error) {
	if strings.TrimSpace(agentID) == "" {
		return protocol.Task{}, ErrTaskInvalid
	}
	if lease < 0 || lease > 5*time.Minute {
		return protocol.Task{}, ErrTaskInvalid
	}
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	now := s.currentTime()
	s.expireLocked(now)
	if lease == 0 {
		lease = s.defaultLease
	}
	for _, id := range s.order {
		task := s.tasks[id]
		if task.Status != protocol.TaskStatusQueued || (task.TargetAgentID != "" && task.TargetAgentID != agentID) {
			continue
		}
		token, err := randomID("lease-")
		if err != nil {
			return protocol.Task{}, err
		}
		task.LeaseToken = token
		task.AgentID = agentID
		task.Status = protocol.TaskStatusDispatched
		task.DispatchedAt = timePtr(now)
		task.LeaseExpiresAt = timePtr(now.Add(lease))
		task.Attempt++
		s.tasks[id] = task
		return cloneTask(task), nil
	}
	return protocol.Task{}, ErrNoTask
}

func (s *TaskStore) ownedLocked(id, agent, token string) (protocol.Task, error) {
	task, ok := s.tasks[id]
	if !ok {
		return protocol.Task{}, ErrTaskNotFound
	}
	if token == "" || token != task.LeaseToken || agent != task.AgentID {
		return protocol.Task{}, ErrTaskLease
	}
	return task, nil
}

func (s *TaskStore) Ack(id, agent, token string) (protocol.Task, error) {
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	now := s.currentTime()
	s.expireLocked(now)
	task, err := s.ownedLocked(id, agent, token)
	if err != nil {
		return protocol.Task{}, err
	}
	if task.Status == protocol.TaskStatusRunning {
		return cloneTask(task), nil
	}
	if task.Status != protocol.TaskStatusDispatched {
		return protocol.Task{}, ErrTaskConflict
	}
	task.Status = protocol.TaskStatusRunning
	task.StartedAt = timePtr(now)
	task.LeaseExpiresAt = timePtr(now.Add(s.defaultLease))
	s.tasks[id] = task
	return cloneTask(task), nil
}

// Renew also returns cancellation state to the worker. It cannot revive a lease.
func (s *TaskStore) Renew(id, agent, token string) (protocol.Task, error) {
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	now := s.currentTime()
	s.expireLocked(now)
	task, err := s.ownedLocked(id, agent, token)
	if err != nil {
		return protocol.Task{}, err
	}
	if task.Status == protocol.TaskStatusCancelling {
		return cloneTask(task), nil
	}
	if task.Status != protocol.TaskStatusRunning {
		return protocol.Task{}, ErrTaskConflict
	}
	task.LeaseExpiresAt = timePtr(now.Add(s.defaultLease))
	s.tasks[id] = task
	return cloneTask(task), nil
}

func (s *TaskStore) Result(id, agent, token string, result protocol.TaskResult) (protocol.Task, error) {
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	now := s.currentTime()
	s.expireLocked(now)
	task, err := s.ownedLocked(id, agent, token)
	if err != nil {
		return protocol.Task{}, err
	}
	if result.Status != protocol.TaskStatusSucceeded && result.Status != protocol.TaskStatusFailed && result.Status != protocol.TaskStatusCancelled {
		return protocol.Task{}, ErrTaskInvalid
	}
	// The server owns timestamps; repeated delivery of identical data is safe.
	result.FinishedAt = time.Time{}
	if terminal(task.Status) && task.Result != nil {
		old := *task.Result
		old.FinishedAt = time.Time{}
		if reflect.DeepEqual(old, result) {
			return cloneTask(task), nil
		}
		return protocol.Task{}, ErrTaskConflict
	}
	if task.Status != protocol.TaskStatusRunning && task.Status != protocol.TaskStatusCancelling {
		return protocol.Task{}, ErrTaskConflict
	}
	if task.Status == protocol.TaskStatusCancelling && result.Status != protocol.TaskStatusCancelled {
		return protocol.Task{}, ErrTaskConflict
	}
	// Execution failures are terminal: a command may have produced side effects.
	// Only dispatches which were never acknowledged may be redelivered.
	result.FinishedAt = now
	result.Artifacts = append([]protocol.Artifact(nil), result.Artifacts...)
	task.Result = &result
	task.Status = result.Status
	task.FinishedAt = timePtr(now)
	task.LeaseExpiresAt = nil
	s.tasks[id] = task
	return cloneTask(task), nil
}

func (s *TaskStore) Cancel(id string) (protocol.Task, error) {
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	now := s.currentTime()
	s.expireLocked(now)
	task, ok := s.tasks[id]
	if !ok {
		return protocol.Task{}, ErrTaskNotFound
	}
	if task.Status == protocol.TaskStatusCancelled || task.Status == protocol.TaskStatusCancelling {
		return cloneTask(task), nil
	}
	if terminal(task.Status) {
		return protocol.Task{}, ErrTaskConflict
	}
	if task.Status == protocol.TaskStatusRunning {
		task.Status = protocol.TaskStatusCancelling
	} else {
		task.Status = protocol.TaskStatusCancelled
		task.FinishedAt = timePtr(now)
		task.LeaseExpiresAt = nil
	}
	s.tasks[id] = task
	return cloneTask(task), nil
}

func (s *TaskStore) Get(id string) (protocol.Task, error) {
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	s.expireLocked(s.currentTime())
	task, ok := s.tasks[id]
	if !ok {
		return protocol.Task{}, ErrTaskNotFound
	}
	return cloneTask(task), nil
}
func (s *TaskStore) List() []protocol.Task {
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	s.expireLocked(s.currentTime())
	out := make([]protocol.Task, 0, len(s.tasks))
	for _, id := range s.order {
		out = append(out, cloneTask(s.tasks[id]))
	}
	return out
}
func terminal(status protocol.TaskStatus) bool {
	switch status {
	case protocol.TaskStatusSucceeded, protocol.TaskStatusFailed, protocol.TaskStatusCancelled, protocol.TaskStatusExpired:
		return true
	}
	return false
}
func (s *TaskStore) expireLocked(now time.Time) {
	s.purgeExpiredArtifactsLocked(now)
	for id, task := range s.tasks {
		if terminal(task.Status) || task.Status == protocol.TaskStatusQueued {
			continue
		}
		timedOut := task.StartedAt != nil && !now.Before(task.StartedAt.Add(time.Duration(task.TimeoutSeconds)*time.Second))
		leaseLost := task.LeaseExpiresAt != nil && !now.Before(*task.LeaseExpiresAt)
		if !timedOut && !leaseLost {
			continue
		}
		if task.Status == protocol.TaskStatusDispatched && task.Attempt < task.MaxAttempts {
			task.Status = protocol.TaskStatusQueued
			task.AgentID = task.TargetAgentID
			task.LeaseToken = ""
			task.DispatchedAt = nil
		} else {
			// Losing contact with a running process does not prove it has stopped.
			task.Status = protocol.TaskStatusExpired
			task.FinishedAt = timePtr(now)
		}
		task.LeaseExpiresAt = nil
		s.tasks[id] = task
	}
}

// purgeExpiredArtifactsLocked removes only inline bytes after their expiry;
// identity, size and hash remain available for audit and downstream storage.
func (s *TaskStore) purgeExpiredArtifactsLocked(now time.Time) {
	for id, task := range s.tasks {
		if task.Result == nil {
			continue
		}
		result := *task.Result
		result.Artifacts = append([]protocol.Artifact(nil), result.Artifacts...)
		changed := false
		for i := range result.Artifacts {
			artifact := &result.Artifacts[i]
			if artifact.DataBase64 != "" && !artifact.ExpiresAt.IsZero() && !now.Before(artifact.ExpiresAt) {
				artifact.DataBase64 = ""
				changed = true
			}
		}
		if changed {
			task.Result = &result
			s.tasks[id] = task
		}
	}
}
func cloneTask(task protocol.Task) protocol.Task {
	task.Action = cloneAction(task.Action)
	task.DispatchedAt = cloneTime(task.DispatchedAt)
	task.StartedAt = cloneTime(task.StartedAt)
	task.FinishedAt = cloneTime(task.FinishedAt)
	task.LeaseExpiresAt = cloneTime(task.LeaseExpiresAt)
	if task.Result != nil {
		r := *task.Result
		r.Artifacts = append([]protocol.Artifact(nil), r.Artifacts...)
		task.Result = &r
	}
	return task
}
func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return timePtr(*t)
}
func cloneAction(action protocol.TaskAction) protocol.TaskAction {
	if action.Command != nil {
		c := *action.Command
		c.Argv = append([]string(nil), c.Argv...)
		c.Environment = cloneMap(c.Environment)
		action.Command = &c
	}
	if action.File != nil {
		f := *action.File
		f.Content = append([]byte(nil), f.Content...)
		action.File = &f
	}
	if action.Screenshot != nil {
		shot := *action.Screenshot
		action.Screenshot = &shot
	}
	return action
}
func timePtr(value time.Time) *time.Time { return &value }
func randomID(prefix string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

type taskSnapshot struct {
	Tasks       map[string]protocol.Task              `json:"tasks"`
	Idempotency map[string]string                     `json:"idempotency"`
	Requests    map[string]protocol.CreateTaskRequest `json:"requests"`
	Order       []string                              `json:"order"`
}

func (s *TaskStore) load() error {
	data, err := os.ReadFile(s.persistencePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snap taskSnapshot
	if err = json.Unmarshal(data, &snap); err != nil {
		return err
	}
	s.tasks = snap.Tasks
	s.idempotency = snap.Idempotency
	s.requests = snap.Requests
	s.order = snap.Order
	if s.tasks == nil {
		s.tasks = map[string]protocol.Task{}
	}
	if s.idempotency == nil {
		s.idempotency = map[string]string{}
	}
	if s.requests == nil {
		s.requests = map[string]protocol.CreateTaskRequest{}
	}
	return nil
}
func (s *TaskStore) persistLocked() {
	if s.persistencePath == "" {
		return
	}
	snap := taskSnapshot{Tasks: s.tasks, Idempotency: s.idempotency, Requests: s.requests, Order: s.order}
	data, err := json.Marshal(snap)
	if err == nil {
		dir := filepath.Dir(s.persistencePath)
		if err = os.MkdirAll(dir, 0700); err == nil {
			tmp, e := os.CreateTemp(dir, "tasks-*.tmp")
			if e == nil {
				name := tmp.Name()
				defer os.Remove(name)
				if _, e = tmp.Write(data); e == nil {
					e = tmp.Chmod(0600)
				}
				if closeErr := tmp.Close(); e == nil {
					e = closeErr
				}
				if e == nil {
					e = os.Rename(name, s.persistencePath)
				}
			}
			err = e
		}
	}
	s.lastPersistenceErr = err
}
func (s *TaskStore) PersistenceError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPersistenceErr
}
func OpenTaskStore(config TaskConfig) (*TaskStore, error) {
	if config.PersistencePath == "" {
		return nil, errors.New("persistence path is required")
	}
	store := NewTaskStore(config)
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}
