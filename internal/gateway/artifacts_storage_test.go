package gateway

import (
	"encoding/base64"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/artifacts"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func TestExternalArtifactStoreServesAndCleansPayload(t *testing.T) {
	root := t.TempDir()
	store, err := artifacts.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{ArtifactStore: store})
	content := []byte("stored-artifact")
	task, err := server.tasks.Create(protocol.CreateTaskRequest{CallerID: "test", Action: protocol.TaskAction{Type: protocol.ActionCommandExec, Command: &protocol.CommandAction{Argv: []string{"true"}}}})
	if err != nil {
		t.Fatal(err)
	}
	task, err = server.tasks.Pull("agent", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.tasks.Ack(task.TaskID, "agent", task.LeaseToken); err != nil {
		t.Fatal(err)
	}
	result := protocol.TaskResult{Status: protocol.TaskStatusSucceeded, Artifacts: []protocol.Artifact{{ArtifactID: "a1", SizeBytes: int64(len(content)), DataBase64: base64.StdEncoding.EncodeToString(content), ExpiresAt: time.Now().Add(time.Hour)}}}
	stored, err := server.tasks.Result(task.TaskID, "agent", task.LeaseToken, result)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Result.Artifacts[0].DataBase64 != "" || stored.Result.Artifacts[0].StorageKey == "" {
		t.Fatalf("artifact was not externalized: %+v", stored.Result.Artifacts[0])
	}
	req := httptest.NewRequest("GET", "/api/v1/tasks/"+task.TaskID+"/artifacts/a1", nil)
	rec := httptest.NewRecorder()
	server.handleArtifactDownload(rec, req, task.TaskID, "a1")
	if rec.Code != 200 || rec.Body.String() != string(content) {
		t.Fatalf("download status=%d body=%q", rec.Code, rec.Body.String())
	}
	server.tasks.mu.Lock()
	expired := server.tasks.tasks[task.TaskID]
	expired.Result.Artifacts[0].ExpiresAt = time.Now().Add(-time.Second)
	storageKey := expired.Result.Artifacts[0].StorageKey
	server.tasks.tasks[task.TaskID] = expired
	server.tasks.mu.Unlock()
	rec = httptest.NewRecorder()
	server.handleArtifactDownload(rec, req, task.TaskID, "a1")
	if rec.Code != 410 {
		t.Fatalf("expired status=%d body=%q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(root + "/" + storageKey); !os.IsNotExist(err) {
		t.Fatalf("expired payload was not deleted: %v", err)
	}
}
