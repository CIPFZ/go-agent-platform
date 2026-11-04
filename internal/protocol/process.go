package protocol

import "time"

// ExecRequest is the body of POST /v1/exec (spec section 6.5).
type ExecRequest struct {
	Argv             []string          `json:"argv"`
	Cwd              string            `json:"cwd"`
	Env              map[string]string `json:"env"`
	EnvClear         bool              `json:"env_clear"`
	Stdin            *string           `json:"stdin"`
	OutputLimitBytes int64             `json:"output_limit_bytes"`
	Detach           bool              `json:"detach"`
	JobTimeoutMS     int64             `json:"job_timeout_ms"`
}

// ExecResult is the result of a synchronous exec. A non-zero exit code is a
// successful response.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`
}

// LaunchRequest is the body of POST /v1/process/launch.
type LaunchRequest struct {
	Argv     []string          `json:"argv"`
	Cwd      string            `json:"cwd"`
	Env      map[string]string `json:"env"`
	EnvClear bool              `json:"env_clear"`
}

// LaunchResult is the result of process/launch.
type LaunchResult struct {
	PID int `json:"pid"`
}

// OpenRequest is the body of POST /v1/process/open: exactly one of Path or
// URL.
type OpenRequest struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

// KillRequest is the body of POST /v1/process/kill.
type KillRequest struct {
	PID     int  `json:"pid"`
	Tree    bool `json:"tree"`
	GraceMS *int `json:"grace_ms"`
	Force   bool `json:"force"`
}

// ExecDetachedResult is the result of exec with detach=true.
type ExecDetachedResult struct {
	JobID string `json:"job_id"`
}

// JobResult is the result of GET /v1/exec/jobs/{job_id}.
type JobResult struct {
	JobID      string     `json:"job_id"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exit_code"`
	Stdout     string     `json:"stdout"`
	Stderr     string     `json:"stderr"`
	Truncated  bool       `json:"truncated"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Reason     string     `json:"reason,omitempty"`
}
