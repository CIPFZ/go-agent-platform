package protocol

import (
	"errors"
	"strings"
	"time"
)

const (
	// Version is the first wire-compatible version of the Agent Platform API.
	Version = "1"
)

var (
	ErrUnsupportedVersion = errors.New("unsupported protocol version")
	ErrMissingRequestID   = errors.New("request id is required")
	ErrMissingTimestamp   = errors.New("timestamp is required")
	ErrMissingAction      = errors.New("task action is required")
	ErrUnsupportedAction  = errors.New("unsupported task action")
)

// Envelope is included in every request and response. Timestamps are always UTC.
type Envelope struct {
	ProtocolVersion string    `json:"protocol_version"`
	RequestID       string    `json:"request_id"`
	Timestamp       time.Time `json:"timestamp"`
}

// Validate checks the fields required on every wire request.
func (e Envelope) Validate() error {
	if e.ProtocolVersion != Version {
		return ErrUnsupportedVersion
	}
	if strings.TrimSpace(e.RequestID) == "" {
		return ErrMissingRequestID
	}
	if e.Timestamp.IsZero() {
		return ErrMissingTimestamp
	}
	return nil
}

// ErrorCode classifies an API failure so callers can decide whether to retry.
type ErrorCode string

const (
	ErrorInvalidRequest     ErrorCode = "invalid_request"
	ErrorUnsupportedVersion ErrorCode = "unsupported_version"
	ErrorUnauthorized       ErrorCode = "unauthorized"
	ErrorNotFound           ErrorCode = "not_found"
	ErrorConflict           ErrorCode = "conflict"
	ErrorRevoked            ErrorCode = "agent_revoked"
	ErrorInternal           ErrorCode = "internal"
)

// APIError is safe to return to API clients and deliberately omits secrets.
type APIError struct {
	Code      ErrorCode         `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable"`
	Details   map[string]string `json:"details,omitempty"`
}

// ErrorResponse wraps failures in the same versioned envelope as successful responses.
type ErrorResponse struct {
	Envelope
	Error APIError `json:"error"`
}

// Capability describes one optional action supported by an agent.
type Capability struct {
	Name       string            `json:"name"`
	Version    string            `json:"version,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// ResourceSummary is intentionally small; richer metrics can be added compatibly later.
type ResourceSummary struct {
	CPUCount      int64 `json:"cpu_count,omitempty"`
	MemoryBytes   int64 `json:"memory_bytes,omitempty"`
	DiskFreeBytes int64 `json:"disk_free_bytes,omitempty"`
}

type AgentStatus string

const (
	AgentStatusOnline  AgentStatus = "online"
	AgentStatusOffline AgentStatus = "offline"
	AgentStatusRevoked AgentStatus = "revoked"
)

// Agent describes the state maintained by the gateway for one stable agent identity.
type Agent struct {
	AgentID       string          `json:"agent_id"`
	AgentVersion  string          `json:"agent_version"`
	Hostname      string          `json:"hostname,omitempty"`
	Capabilities  []Capability    `json:"capabilities,omitempty"`
	Status        AgentStatus     `json:"status"`
	RegisteredAt  time.Time       `json:"registered_at"`
	LastHeartbeat time.Time       `json:"last_heartbeat"`
	LastSeenAt    time.Time       `json:"last_seen_at"`
	Resource      ResourceSummary `json:"resource,omitempty"`
	RevokedAt     *time.Time      `json:"revoked_at,omitempty"`
}

type RegisterRequest struct {
	Envelope
	AgentID      string          `json:"agent_id"`
	AgentVersion string          `json:"agent_version"`
	Hostname     string          `json:"hostname,omitempty"`
	Capabilities []Capability    `json:"capabilities,omitempty"`
	Resource     ResourceSummary `json:"resource,omitempty"`
}

type RegisterResponse struct {
	Envelope
	Agent Agent `json:"agent"`
}

type HeartbeatRequest struct {
	Envelope
	AgentVersion string          `json:"agent_version,omitempty"`
	Capabilities []Capability    `json:"capabilities,omitempty"`
	Resource     ResourceSummary `json:"resource,omitempty"`
}

type HeartbeatResponse struct {
	Envelope
	Agent Agent `json:"agent"`
}

type AgentResponse struct {
	Envelope
	Agent Agent `json:"agent"`
}

type AgentsResponse struct {
	Envelope
	Agents []Agent `json:"agents"`
}

// TaskStatus is the server-owned lifecycle state for one unit of work.
type TaskStatus string

const (
	TaskStatusQueued     TaskStatus = "queued"
	TaskStatusDispatched TaskStatus = "dispatched"
	TaskStatusRunning    TaskStatus = "running"
	TaskStatusSucceeded  TaskStatus = "succeeded"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusCancelled  TaskStatus = "cancelled"
	TaskStatusCancelling TaskStatus = "cancelling"
	TaskStatusExpired    TaskStatus = "expired"
)

type ActionType string

const (
	ActionCommandExec       ActionType = "command.exec"
	ActionFileRead          ActionType = "file.read"
	ActionFileWrite         ActionType = "file.write"
	ActionDesktopScreenshot ActionType = "desktop.screenshot"
)

type CommandAction struct {
	Argv            []string          `json:"argv"`
	WorkingDir      string            `json:"working_dir,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	TimeoutSeconds  int64             `json:"timeout_seconds,omitempty"`
	OutputLimitByte int64             `json:"output_limit_bytes,omitempty"`
}

type FileAction struct {
	Path      string `json:"path"`
	Content   []byte `json:"content,omitempty"`
	MaxBytes  int64  `json:"max_bytes,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type ScreenshotAction struct {
	OutputPath string `json:"output_path,omitempty"`
	Format     string `json:"format,omitempty"`
}

type TaskAction struct {
	Type       ActionType        `json:"type"`
	Command    *CommandAction    `json:"command,omitempty"`
	File       *FileAction       `json:"file,omitempty"`
	Screenshot *ScreenshotAction `json:"screenshot,omitempty"`
}

type Artifact struct {
	ArtifactID string    `json:"artifact_id"`
	Name       string    `json:"name,omitempty"`
	MediaType  string    `json:"media_type,omitempty"`
	SizeBytes  int64     `json:"size_bytes,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	DataBase64 string    `json:"data_base64,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

type Task struct {
	TaskID         string      `json:"task_id"`
	LeaseToken     string      `json:"lease_token,omitempty"`
	TargetAgentID  string      `json:"target_agent_id,omitempty"`
	CallerID       string      `json:"caller_id,omitempty"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	AgentID        string      `json:"agent_id,omitempty"`
	Action         TaskAction  `json:"action"`
	Status         TaskStatus  `json:"status"`
	CreatedAt      time.Time   `json:"created_at"`
	DispatchedAt   *time.Time  `json:"dispatched_at,omitempty"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	FinishedAt     *time.Time  `json:"finished_at,omitempty"`
	LeaseExpiresAt *time.Time  `json:"lease_expires_at,omitempty"`
	TimeoutSeconds int64       `json:"timeout_seconds,omitempty"`
	Attempt        int         `json:"attempt"`
	MaxAttempts    int         `json:"max_attempts"`
	Result         *TaskResult `json:"result,omitempty"`
}

type TaskResult struct {
	Status       TaskStatus `json:"status"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	ExitCode     int        `json:"exit_code,omitempty"`
	Stdout       string     `json:"stdout,omitempty"`
	Stderr       string     `json:"stderr,omitempty"`
	Artifacts    []Artifact `json:"artifacts,omitempty"`
	FinishedAt   time.Time  `json:"finished_at"`
}

type CreateTaskRequest struct {
	Envelope
	CallerID       string     `json:"caller_id"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	AgentID        string     `json:"agent_id,omitempty"`
	Action         TaskAction `json:"action"`
	TimeoutSeconds int64      `json:"timeout_seconds,omitempty"`
	MaxAttempts    int        `json:"max_attempts,omitempty"`
}

type TaskResponse struct {
	Envelope
	Task Task `json:"task"`
}

type TasksResponse struct {
	Envelope
	Tasks []Task `json:"tasks"`
}

type PullTasksRequest struct {
	Envelope
	LeaseSeconds int64 `json:"lease_seconds,omitempty"`
}

type TaskResultRequest struct {
	Envelope
	AgentID    string     `json:"agent_id"`
	LeaseToken string     `json:"lease_token"`
	Result     TaskResult `json:"result"`
}

func (a TaskAction) Validate() error {
	switch a.Type {
	case ActionCommandExec:
		if a.Command == nil || len(a.Command.Argv) == 0 || strings.TrimSpace(a.Command.Argv[0]) == "" {
			return ErrMissingAction
		}
	case ActionFileRead, ActionFileWrite:
		if a.File == nil || strings.TrimSpace(a.File.Path) == "" {
			return ErrMissingAction
		}
	case ActionDesktopScreenshot:
		if a.Screenshot == nil {
			return ErrMissingAction
		}
	default:
		return ErrUnsupportedAction
	}
	return nil
}

// TaskLeaseRequest fences every worker mutation to one dispatch attempt.
type TaskLeaseRequest struct {
	Envelope
	AgentID    string `json:"agent_id"`
	LeaseToken string `json:"lease_token"`
}
