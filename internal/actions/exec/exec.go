// Package exec runs commands for the /v1/exec endpoints. Commands are argv
// arrays executed without a shell (spec D7) inside a process tree that is
// terminated as a unit on timeout, disconnect or shutdown (spec D4).
package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
)

const (
	// DefaultOutputLimit and MaxOutputLimit bound stdout and stderr each
	// (spec section 7).
	DefaultOutputLimit int64 = 1 << 20
	MaxOutputLimit     int64 = 64 << 20
)

// Spec describes one command invocation.
type Spec struct {
	Argv     []string
	Cwd      string
	Env      map[string]string
	EnvClear bool
	Stdin    []byte
	// OutputLimit caps stdout and stderr separately; <=0 means the default.
	OutputLimit int64
}

// Result is the outcome of a command that ran to completion. A non-zero exit
// code is a normal result, not an error.
type Result struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	Duration  time.Duration
}

// ErrInvalidEnv reports an environment key or value the OS cannot represent.
var ErrInvalidEnv = errors.New("environment keys must be non-empty and must not contain '=' or NUL")

// OutputLimit clamps a requested per-stream output limit to spec bounds.
func OutputLimit(requested int64) int64 {
	switch {
	case requested <= 0:
		return DefaultOutputLimit
	case requested > MaxOutputLimit:
		return MaxOutputLimit
	default:
		return requested
	}
}

// Command builds an *exec.Cmd for spec without starting it.
func Command(spec Spec) (*osexec.Cmd, error) {
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, errors.New("argv must contain at least the program")
	}
	env, err := Environ(spec.Env, spec.EnvClear)
	if err != nil {
		return nil, err
	}
	cmd := osexec.Command(spec.Argv[0], spec.Argv[1:]...)
	// exec.Command resolves argv[0] against PATH at construction; with
	// env_clear or an overridden PATH the request's own PATH must win.
	if path, ok := lookupEnv(env, "PATH"); ok {
		if resolved, lookErr := lookPath(spec.Argv[0], path); lookErr == nil {
			cmd.Path = resolved
			cmd.Err = nil
		}
	}
	cmd.Env = env
	cmd.Dir = spec.Cwd
	if cmd.Dir == "" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			cmd.Dir = home
		}
	}
	return cmd, nil
}

// Run executes spec synchronously. Cancelling ctx terminates the whole process
// tree; the returned error is then ctx.Err().
func Run(ctx context.Context, spec Spec) (Result, error) {
	cmd, err := Command(spec)
	if err != nil {
		return Result{}, err
	}
	limit := OutputLimit(spec.OutputLimit)
	stdout, stderr := &LimitedBuffer{Limit: limit}, &LimitedBuffer{Limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	started := time.Now()
	tree, err := process.StartTree(cmd)
	if err != nil {
		return Result{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = tree.Kill() })
	waitErr := tree.Wait()
	stop()
	result := Result{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: stdout.Truncated || stderr.Truncated,
		Duration:  time.Since(started),
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if waitErr != nil {
		var exitErr *osexec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return result, waitErr
		}
	}
	result.ExitCode = cmd.ProcessState.ExitCode()
	return result, nil
}

// Environ merges overrides into the current environment, or returns only the
// overrides when clear is set. Keys compare case-insensitively on Windows.
func Environ(overrides map[string]string, clear bool) ([]string, error) {
	for key, value := range overrides {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, ErrInvalidEnv
		}
	}
	var base []string
	if !clear {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if _, overridden := lookupKey(overrides, key); overridden {
			continue
		}
		env = append(env, item)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env, nil
}

func lookupKey(env map[string]string, key string) (string, bool) {
	if runtime.GOOS != "windows" {
		value, ok := env[key]
		return value, ok
	}
	for k, v := range env {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func lookupEnv(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		k, v, _ := strings.Cut(env[i], "=")
		if k == key || (runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			return v, true
		}
	}
	return "", false
}

// LimitedBuffer keeps the first Limit bytes written and records whether more
// arrived. Writes never fail so the child is not killed by a broken pipe.
type LimitedBuffer struct {
	Limit     int64
	Truncated bool
	data      bytes.Buffer
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	remaining := b.Limit - int64(b.data.Len())
	if remaining <= 0 {
		if len(p) > 0 {
			b.Truncated = true
		}
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.data.Write(p[:remaining])
		b.Truncated = true
		return len(p), nil
	}
	return b.data.Write(p)
}

// Bytes returns the retained output.
func (b *LimitedBuffer) Bytes() []byte { return b.data.Bytes() }
