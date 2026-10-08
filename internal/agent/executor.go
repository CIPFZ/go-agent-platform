package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	cleanup, runCommand := configureProcess(command)
	defer cleanup()
	command.Dir = e.Workspace
	if action.WorkingDir != "" {
		path, err := e.safePath(action.WorkingDir)
		if err != nil {
			return failedResult("invalid_working_dir", err)
		}
		command.Dir = path
	}
	env := make(map[string]string, len(os.Environ())+len(action.Environment)+1)
	for _, item := range os.Environ() {
		key, value, found := strings.Cut(item, "=")
		if found {
			env[key] = value
		}
	}
	env["HOME"] = e.Workspace
	env["LANG"] = "C.UTF-8"
	for key, value := range action.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return failedResult("invalid_environment", fmt.Errorf("invalid environment key %q", key))
		}
		env[key] = value
	}
	command.Env = make([]string, 0, len(env))
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
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
	err := runCommand()
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
	if limit <= 0 || limit > 2<<20 {
		limit = 2 << 20
	}
	file, err := os.Open(path)
	if err != nil {
		return failedResult("file_read", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return failedResult("file_read", errors.New("path must be a regular file"))
	}
	if info.Size() > limit {
		return failedResult("file_too_large", fmt.Errorf("file is %d bytes, limit is %d", info.Size(), limit))
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return failedResult("file_read", err)
	}
	if int64(len(content)) > limit {
		return failedResult("file_too_large", errors.New("file grew past the read limit"))
	}
	preview := string(content)
	if len(content) > 1024 {
		preview = ""
	}
	return protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Stdout: preview, Artifacts: []protocol.Artifact{artifactFor(path, content)}}
}

func (e Executor) executeFileWrite(action *protocol.FileAction) protocol.TaskResult {
	path, err := e.safePath(action.Path)
	if err != nil {
		return failedResult("invalid_path", err)
	}
	limit := e.MaxFileBytes
	if limit <= 0 || limit > 2<<20 {
		limit = 2 << 20
	}
	if int64(len(action.Content)) > limit {
		return failedResult("file_too_large", fmt.Errorf("content exceeds %d bytes", limit))
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
	if err := writeFileAtomic(path, action.Content, action.Overwrite); err != nil {
		if errors.Is(err, os.ErrExist) {
			return failedResult("file_exists", err)
		}
		return failedResult("file_write", err)
	}
	return protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Artifacts: []protocol.Artifact{artifactFor(path, action.Content)}}
}

// Publish a complete file in one filesystem operation. Link provides atomic
// create-if-absent semantics even when multiple workers target the same path.
func writeFileAtomic(path string, content []byte, overwrite bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if overwrite {
		return os.Rename(file.Name(), path)
	}
	return os.Link(file.Name(), path)
}

func (e Executor) executeScreenshot(ctx context.Context, action *protocol.ScreenshotAction) protocol.TaskResult {
	if !desktopSessionAvailable() {
		return failedResult("no_display", errors.New("no graphical display session is available"))
	}
	path, err := e.safePath(action.OutputPath)
	if err != nil {
		return failedResult("invalid_path", err)
	}
	if action.OutputPath == "" {
		return failedResult("invalid_path", errors.New("screenshot output_path is required"))
	}
	if err := captureDesktopScreenshot(ctx, path, e.ScreenshotCommand); err != nil {
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
