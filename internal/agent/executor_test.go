package agent

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	decoded, err := base64.StdEncoding.DecodeString(read.Artifacts[0].DataBase64)
	if err != nil || string(decoded) != string(content) || read.Artifacts[0].ArtifactID == "" {
		t.Fatalf("artifact payload=%+v err=%v", read.Artifacts[0], err)
	}
	escape := executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionFileRead, File: &protocol.FileAction{Path: "../outside"}}})
	if escape.Status != protocol.TaskStatusFailed || !strings.Contains(escape.ErrorMessage, "outside") {
		t.Fatalf("escape result=%+v", escape)
	}
	outside := t.TempDir()
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linked := executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionFileWrite, File: &protocol.FileAction{Path: "link/escape.txt", Content: []byte("escape"), Overwrite: true}}})
	if linked.Status != protocol.TaskStatusFailed || !strings.Contains(linked.ErrorMessage, "outside") {
		t.Fatalf("symlink escape result=%+v", linked)
	}
}

func TestExecutorFileWriteIsAtomicForConcurrentCreate(t *testing.T) {
	workspace := t.TempDir()
	executor := Executor{Workspace: workspace}
	const writers = 8
	results := make(chan protocol.TaskResult, writers)
	var group sync.WaitGroup
	for i := 0; i < writers; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			results <- executor.Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionFileWrite, File: &protocol.FileAction{
				Path: "shared.txt", Content: []byte(strings.Repeat(string(rune('a'+i)), 128)),
			}}})
		}(i)
	}
	group.Wait()
	close(results)
	succeeded := 0
	for result := range results {
		if result.Status == protocol.TaskStatusSucceeded {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent create successes=%d, want one", succeeded)
	}
}

func TestExecutorScreenshotRequiresDisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	result := (Executor{Workspace: t.TempDir(), ScreenshotCommand: "true"}).Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionDesktopScreenshot, Screenshot: &protocol.ScreenshotAction{OutputPath: "capture.png"}}})
	if result.Status != protocol.TaskStatusFailed || result.ErrorCode != "no_display" {
		t.Fatalf("screenshot result=%+v", result)
	}
}

func TestExecutorTimeoutCleansProcessGroup(t *testing.T) {
	result := (Executor{Workspace: t.TempDir()}).Execute(context.Background(), protocol.Task{Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{
		Argv: []string{"sh", "-c", "sleep 30 & wait"}, TimeoutSeconds: 1,
	}}})
	if result.Status != protocol.TaskStatusFailed || result.ErrorCode != "timeout" {
		t.Fatalf("timeout result=%+v", result)
	}
}
