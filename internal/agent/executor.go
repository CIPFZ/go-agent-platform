package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/policy"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Executor runs the P3/P4 actions in a bounded workspace. It never invokes a
// shell for command.exec and rejects a small set of host destructive commands.
type Executor struct {
	Workspace         string
	MaxOutputBytes    int64
	MaxFileBytes      int64
	ScreenshotCommand string
	AllowCommands     map[string]bool
	Policy            *policy.Engine
}

func (e Executor) Execute(ctx context.Context, task protocol.Task) protocol.TaskResult {
	if err := task.Action.Validate(); err != nil {
		return failedResult("invalid_action", err)
	}
	if e.Policy != nil {
		decision := e.Policy.Evaluate(task.Action)
		if decision.Decision != policy.DecisionAllow {
			return failedResult("policy_"+string(decision.Decision), errors.New(decision.Reason))
		}
	}
	switch task.Action.Type {
	case protocol.ActionCommandExec:
		return e.executeCommand(ctx, task.Action.Command)
	case protocol.ActionFileRead:
		return e.executeFileRead(task.Action.File)
	case protocol.ActionFileWrite:
		return e.executeFileWrite(task.Action.File)
	case protocol.ActionDesktopScreenshot:
		return e.executeScreenshot(ctx, task.Action.Screenshot)
	default:
		return failedResult("unsupported_action", protocol.ErrUnsupportedAction)
	}
}

func (e Executor) executeCommand(parent context.Context, action *protocol.CommandAction) protocol.TaskResult {
	program := action.Argv[0]
	if !e.commandAllowed(program) {
		return failedResult("policy_denied", fmt.Errorf("command %q is not allowed", program))
	}
	timeout := time.Duration(action.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, program, action.Argv[1:]...)
	if action.WorkingDir != "" {
		path, err := e.safePath(action.WorkingDir)
		if err != nil {
			return failedResult("invalid_working_dir", err)
		}
		command.Dir = path
	}
	if len(action.Environment) > 0 {
		command.Env = append([]string{}, os.Environ()...)
		for key, value := range action.Environment {
			if strings.ContainsAny(key, "=\x00") {
				return failedResult("invalid_environment", fmt.Errorf("invalid environment key %q", key))
			}
			command.Env = append(command.Env, key+"="+value)
		}
	}
	limit := action.OutputLimitByte
	if limit <= 0 || (e.MaxOutputBytes > 0 && limit > e.MaxOutputBytes) {
		limit = e.MaxOutputBytes
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	stdout, stderr := &limitedBuffer{limit: limit}, &limitedBuffer{limit: limit}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	result := protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Stdout: stdout.String(), Stderr: stderr.String()}
	if stdout.truncated || stderr.truncated {
		result.ErrorCode = "output_limited"
	}
	if err != nil {
		result.Status = protocol.TaskStatusFailed
		result.ErrorCode = "command_failed"
		result.ErrorMessage = err.Error()
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitCode()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.ErrorCode = "timeout"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			result.ErrorCode = "cancelled"
		}
	}
	return result
}

func (e Executor) executeFileRead(action *protocol.FileAction) protocol.TaskResult {
	path, err := e.safePath(action.Path)
	if err != nil {
		return failedResult("invalid_path", err)
	}
	limit := action.MaxBytes
	if limit <= 0 || (e.MaxFileBytes > 0 && limit > e.MaxFileBytes) {
		limit = e.MaxFileBytes
	}
	if limit <= 0 {
		limit = 16 << 20
	}
	info, err := os.Stat(path)
	if err != nil {
		return failedResult("file_read", err)
	}
	if info.Size() > limit {
		return failedResult("file_too_large", fmt.Errorf("file is %d bytes, limit is %d", info.Size(), limit))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return failedResult("file_read", err)
	}
	return protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Stdout: string(content), Artifacts: []protocol.Artifact{artifactFor(path, content)}}
}

func (e Executor) executeFileWrite(action *protocol.FileAction) protocol.TaskResult {
	path, err := e.safePath(action.Path)
	if err != nil {
		return failedResult("invalid_path", err)
	}
	if e.MaxFileBytes > 0 && int64(len(action.Content)) > e.MaxFileBytes {
		return failedResult("file_too_large", fmt.Errorf("content exceeds %d bytes", e.MaxFileBytes))
	}
	if !action.Overwrite {
		if _, statErr := os.Stat(path); statErr == nil {
			return failedResult("file_exists", os.ErrExist)
		}
	}
	if action.SHA256 != "" {
		sum := sha256.Sum256(action.Content)
		if !strings.EqualFold(action.SHA256, hex.EncodeToString(sum[:])) {
			return failedResult("hash_mismatch", errors.New("content SHA-256 does not match request"))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return failedResult("file_write", err)
	}
	if err := os.WriteFile(path, action.Content, 0o600); err != nil {
		return failedResult("file_write", err)
	}
	return protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Artifacts: []protocol.Artifact{artifactFor(path, action.Content)}}
}

func (e Executor) executeScreenshot(ctx context.Context, action *protocol.ScreenshotAction) protocol.TaskResult {
	if strings.TrimSpace(os.Getenv("DISPLAY")) == "" && strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) == "" {
		return failedResult("no_display", errors.New("no graphical display session is available"))
	}
	commandName := e.ScreenshotCommand
	if commandName == "" {
		commandName = "gnome-screenshot"
	}
	path, err := e.safePath(action.OutputPath)
	if err != nil {
		return failedResult("invalid_path", err)
	}
	if action.OutputPath == "" {
		return failedResult("invalid_path", errors.New("screenshot output_path is required"))
	}
	if err := exec.CommandContext(ctx, commandName, "-f", path).Run(); err != nil {
		return failedResult("screenshot", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return failedResult("screenshot", err)
	}
	mediaType := "image/png"
	if action.Format != "" {
		mediaType = "image/" + strings.ToLower(action.Format)
	}
	artifact := artifactFor(path, content)
	artifact.MediaType = mediaType
	return protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Artifacts: []protocol.Artifact{artifact}}
}

func (e Executor) commandAllowed(program string) bool {
	base := filepath.Base(program)
	if len(e.AllowCommands) > 0 {
		return e.AllowCommands[base] || e.AllowCommands[program]
	}
	switch base {
	case "sudo", "su", "rm", "rmdir", "shutdown", "reboot", "poweroff", "mkfs", "mount", "umount", "useradd", "userdel", "passwd":
		return false
	default:
		return true
	}
}

func (e Executor) safePath(input string) (string, error) {
	if strings.TrimSpace(e.Workspace) == "" {
		return "", errors.New("executor workspace is required")
	}
	root, err := filepath.Abs(e.Workspace)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve the existing target, or the nearest existing parent for a new
	// file. This prevents a symlink inside the workspace from escaping it.
	resolved, resolveErr := filepath.EvalSymlinks(path)
	if resolveErr != nil {
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
		probe := filepath.Dir(path)
		suffix := []string{filepath.Base(path)}
		for {
			parent, parentErr := filepath.EvalSymlinks(probe)
			if parentErr == nil {
				for i := len(suffix) - 1; i >= 0; i-- {
					parent = filepath.Join(parent, suffix[i])
				}
				resolved = parent
				break
			}
			next := filepath.Dir(probe)
			if next == probe {
				return "", parentErr
			}
			suffix = append(suffix, filepath.Base(probe))
			probe = next
		}
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("path is outside executor workspace")
	}
	return path, nil
}

func failedResult(code string, err error) protocol.TaskResult {
	return protocol.TaskResult{Status: protocol.TaskStatusFailed, ErrorCode: code, ErrorMessage: err.Error()}
}

func artifactFor(path string, content []byte) protocol.Artifact {
	sum := sha256.Sum256(content)
	artifact := protocol.Artifact{ArtifactID: "artifact-" + hex.EncodeToString(sum[:8]), Name: filepath.Base(path), SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	if len(content) <= 2<<20 {
		artifact.DataBase64 = base64.StdEncoding.EncodeToString(content)
	}
	return artifact
}

type limitedBuffer struct {
	data      bytes.Buffer
	limit     int64
	truncated bool
}

func (b *limitedBuffer) String() string { return b.data.String() }
func (b *limitedBuffer) Len() int       { return b.data.Len() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - int64(b.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.data.Write(p)
}
