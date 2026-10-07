package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"os"
	"strings"
	"sync"
	"time"
)

// Runner maintains independent heartbeats and a bounded worker pool.
type Runner struct {
	Client            *Client
	HeartbeatInterval time.Duration
	ShutdownTimeout   time.Duration
	TaskPollInterval  time.Duration
	LeaseSeconds      int64
	Workers           int
	Executor          TaskExecutor
}

// TaskExecutor is the execution boundary used by Runner. Keeping it small
// lets deployments provide a specialized executor and lets cancellation tests
// exercise the worker lifecycle without starting host processes.
type TaskExecutor interface {
	Execute(context.Context, protocol.Task) protocol.TaskResult
}

func (r *Runner) Run(parent context.Context) error {
	if r == nil || r.Client == nil {
		return errors.New("agent client is required")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := retry(ctx, func() error { _, err := r.Client.Register(ctx); return err }); err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	defer r.offline()
	interval := r.HeartbeatInterval
	if interval <= 0 {
		interval = r.Client.config.HeartbeatInterval
	}
	poll := r.TaskPollInterval
	if poll <= 0 {
		poll = time.Second
	}
	leaseSeconds := r.LeaseSeconds
	if leaseSeconds <= 0 {
		leaseSeconds = 30
	}
	workers := r.Workers
	if workers <= 0 {
		workers = 1
	}
	if workers > 64 {
		return errors.New("workers must not exceed 64")
	}
	errorsCh := make(chan error, workers+1)
	var wg sync.WaitGroup
	launch := func(fn func() error) { wg.Add(1); go func() { defer wg.Done(); errorsCh <- fn() }() }
	launch(func() error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				err := retry(ctx, func() error {
					_, err := r.Client.Heartbeat(ctx)
					var h *HTTPError
					if errors.As(err, &h) && h.StatusCode == 404 {
						_, err = r.Client.Register(ctx)
					}
					return err
				})
				if err != nil {
					return fmt.Errorf("heartbeat: %w", err)
				}
			}
		}
	})
	if r.Executor != nil {
		for i := 0; i < workers; i++ {
			launch(func() error {
				for {
					err := retry(ctx, func() error { return r.processOneTask(ctx, leaseSeconds) })
					if err != nil {
						return err
					}
					if err = waitContext(ctx, poll); err != nil {
						return err
					}
				}
			})
		}
	}
	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-errorsCh:
	}
	cancel()
	wg.Wait()
	return err
}
func (r *Runner) offline() {
	timeout := r.ShutdownTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = r.Client.Offline(ctx)
}

func (r *Runner) processOneTask(ctx context.Context, leaseSeconds int64) error {
	task, err := r.Client.PullTask(ctx, leaseSeconds)
	if errors.Is(err, ErrNoTask) {
		return nil
	}
	if err != nil {
		return err
	}
	// A repeated acknowledgement is safe because the lease token is immutable.
	if err = retry(ctx, func() error { var e error; task, e = r.Client.AckTask(ctx, task.TaskID, task.LeaseToken); return e }); err != nil {
		return err
	}
	taskCtx, cancel := context.WithCancel(ctx)
	if task.TimeoutSeconds > 0 {
		cancel()
		taskCtx, cancel = context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	done := make(chan protocol.TaskResult, 1)
	go func() { done <- r.Executor.Execute(taskCtx, task) }()
	leaseExpires := time.Now().Add(30 * time.Second)
	if task.LeaseExpiresAt != nil {
		leaseExpires = *task.LeaseExpiresAt
	}
	renewEvery := time.Until(leaseExpires) / 3
	if renewEvery > time.Second {
		renewEvery = time.Second
	}
	if renewEvery < 10*time.Millisecond {
		renewEvery = 10 * time.Millisecond
	}
	ticker := time.NewTicker(renewEvery)
	defer ticker.Stop()
	leaseDeadline := leaseExpires
	cancelled := false
	var result protocol.TaskResult
	for {
		select {
		case result = <-done:
			if cancelled {
				result.Status = protocol.TaskStatusCancelled
				result.ErrorCode = "cancelled"
			}
			// Cleanup result delivery uses its own bounded context on service shutdown.
			sendCtx, stop := context.WithTimeout(context.Background(), r.Client.config.RequestTimeout)
			defer stop()
			err = retry(sendCtx, func() error {
				_, e := r.Client.SubmitTaskResult(sendCtx, task.TaskID, task.LeaseToken, result)
				return e
			})
			var h *HTTPError
			if errors.As(err, &h) && h.StatusCode == 409 {
				return nil
			} // lease/cancellation won the race
			return err
		case <-taskCtx.Done():
			// Executors should observe cancellation, but a broken plugin must not
			// hold the worker forever during shutdown or lease loss.
			select {
			case result = <-done:
			case <-time.After(r.shutdownGrace()):
				result = protocol.TaskResult{Status: protocol.TaskStatusCancelled, ErrorCode: "cancelled", ErrorMessage: "executor did not stop after cancellation"}
			}
			if cancelled || ctx.Err() != nil {
				result.Status = protocol.TaskStatusCancelled
				result.ErrorCode = "cancelled"
			}
			sendCtx, stop := context.WithTimeout(context.Background(), r.Client.config.RequestTimeout)
			_, _ = r.Client.SubmitTaskResult(sendCtx, task.TaskID, task.LeaseToken, result)
			stop()
			return ctx.Err()
		case <-ticker.C:
			renewCtx, stop := context.WithDeadline(ctx, leaseDeadline)
			renewed, e := r.Client.RenewTask(renewCtx, task.TaskID, task.LeaseToken)
			stop()
			if e != nil {
				if !temporary(e) || !time.Now().Before(leaseDeadline) {
					cancel()
					_, _ = r.awaitResult(done)
					var h *HTTPError
					if errors.As(e, &h) && h.StatusCode == 409 {
						return nil
					}
					return e
				}
				continue
			}
			if renewed.Status == protocol.TaskStatusCancelling {
				cancelled = true
				cancel()
			} else if renewed.LeaseExpiresAt != nil {
				leaseDeadline = *renewed.LeaseExpiresAt
			}
		}
	}
}

func (r *Runner) awaitResult(done <-chan protocol.TaskResult) (protocol.TaskResult, bool) {
	select {
	case result := <-done:
		return result, true
	case <-time.After(r.shutdownGrace()):
		return protocol.TaskResult{Status: protocol.TaskStatusCancelled, ErrorCode: "cancelled", ErrorMessage: "executor did not stop after lease loss"}, false
	}
}

func (r *Runner) shutdownGrace() time.Duration {
	grace := r.ShutdownTimeout
	if grace <= 0 || grace > 5*time.Second {
		grace = 5 * time.Second
	}
	return grace
}

func retry(ctx context.Context, fn func() error) error {
	delay := 100 * time.Millisecond
	for {
		err := fn()
		if err == nil || !temporary(err) {
			return err
		}
		if err = waitContext(ctx, delay); err != nil {
			return err
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
}
func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
