package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestExecutorCommandAndOutputLimit(t *testing.T) {
	executor := Executor{Workspace: t.TempDir(), MaxOutputBytes: 4}
	result := executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"printf", "abcdef"}}}})
	if result.Status != protocol.TaskStatusSucceeded || result.Stdout != "abcd" || result.ErrorCode != "output_limited" {
		t.Fatalf("limited command result=%+v", result)
	}
	denied := executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"rm", "-rf", "/"}}}})
	if denied.Status != protocol.TaskStatusFailed || denied.ErrorCode != "policy_denied" {
		t.Fatalf("denied command result=%+v", denied)
	}
}

func TestExecutorFileSandboxAndHash(t *testing.T) {
	workspace := t.TempDir()
	executor := Executor{Workspace: workspace, MaxFileBytes: 64}
	content := []byte("hello")
	written := executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionFileWrite, File: &protocol.FileAction{Path: "nested/file.txt", Content: content, Overwrite: true}}})
	if written.Status != protocol.TaskStatusSucceeded {
		t.Fatalf("write result=%+v", written)
	}
	read := executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionFileRead, File: &protocol.FileAction{Path: "nested/file.txt"}}})
	if read.Status != protocol.TaskStatusSucceeded || read.Stdout != string(content) || len(read.Artifacts) != 1 {
		t.Fatalf("read result=%+v", read)
	}
	escape := executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionFileRead, File: &protocol.FileAction{Path: "../outside"}}})
	if escape.Status != protocol.TaskStatusFailed || !strings.Contains(escape.ErrorMessage, "outside") {
		t.Fatalf("escape result=%+v", escape)
	}
}
