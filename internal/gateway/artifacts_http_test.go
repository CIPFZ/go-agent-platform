package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestHTTPArtifactDownloadHonorsHashAndExpiry(t *testing.T) {
	server := NewServer(Config{Now: time.Now})
	task, err := server.tasks.Create(protocol.CreateTaskRequest{CallerID: "test", Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"true"}}}})
	if err != nil {
		t.Fatal(err)
	}
	task, err = server.tasks.Pull("agent", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.tasks.Ack(task.TaskID, "agent", task.LeaseToken); err != nil {
		t.Fatal(err)
	}
	content := "artifact-content"
	if _, err = server.tasks.Result(task.TaskID, "agent", task.LeaseToken, protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Artifacts: []protocol.Artifact{{ArtifactID: "a1", MediaType: "text/plain", SizeBytes: int64(len(content)), SHA256: "e7ba55ec4b1cbb1e2a7f7c8a959e545f4db0230bf1e0a84396990601a1cd63ed", DataBase64: "YXJ0aWZhY3QtY29udGVudA==", ExpiresAt: time.Now().Add(time.Hour)}}}); err != nil {
		t.Fatal(err)
	}
	h := server.Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+task.TaskID+"/artifacts/a1", nil)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"e7ba55ec4b1cbb1e2a7f7c8a959e545f4db0230bf1e0a84396990601a1cd63ed"` {
		t.Fatalf("download status=%d headers=%v", recorder.Code, recorder.Header())
	}
	body, _ := io.ReadAll(recorder.Result().Body)
	if string(body) != content {
		t.Fatalf("download body=%q", body)
	}
	if !strings.EqualFold(recorder.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("content type=%q", recorder.Header().Get("Content-Type"))
	}

	server.tasks.mu.Lock()
	stored := server.tasks.tasks[task.TaskID]
	stored.Result.Artifacts[0].ExpiresAt = time.Now().Add(-time.Second)
	server.tasks.tasks[task.TaskID] = stored
	server.tasks.mu.Unlock()
	recorder = httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusGone {
		t.Fatalf("expired status=%d", recorder.Code)
	}
}
