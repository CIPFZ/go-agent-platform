package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Runner owns the registration and heartbeat lifecycle of one guest agent.
type Runner struct {
	Client            *Client
	HeartbeatInterval time.Duration
	ShutdownTimeout   time.Duration
	TaskPollInterval  time.Duration
	Executor          *Executor
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.Client == nil {
		return errors.New("agent client is required")
	}
	interval := r.HeartbeatInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	pollInterval := r.TaskPollInterval
	if pollInterval <= 0 {
		pollInterval = interval
	}
	if _, err := r.Client.Register(ctx); err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	defer r.offline()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastTaskPoll := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := r.Client.Heartbeat(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("heartbeat agent: %w", err)
			}
			if r.Executor != nil && (lastTaskPoll.IsZero() || time.Since(lastTaskPoll) >= pollInterval) {
				lastTaskPoll = time.Now()
				if err := r.processOneTask(ctx); err != nil {
					return err
				}
			}
		}
	}
}

func (r *Runner) offline() {
	shutdownTimeout := r.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = 3 * time.Second
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	_, _ = r.Client.Offline(shutdownContext)
	cancel()
}

func (r *Runner) processOneTask(ctx context.Context) error {
	task, err := r.Client.PullTask(ctx, 30)
	if errors.Is(err, ErrNoTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pull task: %w", err)
	}
	if _, err := r.Client.AckTask(ctx, task.TaskID); err != nil {
		return fmt.Errorf("ack task: %w", err)
	}
	taskContext := ctx
	cancel := func() {}
	if task.TimeoutSeconds > 0 {
		taskContext, cancel = context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	}
	result := r.Executor.Execute(taskContext, task)
	cancel()
	if _, err := r.Client.SubmitTaskResult(ctx, task.TaskID, result); err != nil {
		return fmt.Errorf("submit task result: %w", err)
	}
	return nil
}

// LoadOrCreateID persists the identity so an agent restart does not register a new identity.
func LoadOrCreateID(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("state file path is required")
	}
	if content, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(content))
		if id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent state: %w", err)
	}
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return "", fmt.Errorf("create agent state directory: %w", err)
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write agent state: %w", err)
	}
	return id, nil
}

func filepathDir(path string) string {
	index := strings.LastIndex(path, string(os.PathSeparator))
	if index < 0 {
		return "."
	}
	if index == 0 {
		return string(os.PathSeparator)
	}
	return path[:index]
}

func newID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate agent ID: %w", err)
	}
	return "agent-" + hex.EncodeToString(buffer), nil
}
