package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Validate and normalize before writing anything. Storage keys are scoped to
// both task and content, and may never be supplied by an Agent.
func (s *TaskStore) prepareArtifacts(task protocol.Task, result protocol.TaskResult) (protocol.TaskResult, map[string][]byte, error) {
	result.Artifacts = append([]protocol.Artifact(nil), result.Artifacts...)
	payloads := make(map[string][]byte)
	ids := make(map[string]bool)
	for i := range result.Artifacts {
		a := &result.Artifacts[i]
		if a.ArtifactID == "" || ids[a.ArtifactID] || a.StorageKey != "" {
			return result, nil, fmt.Errorf("%w: invalid artifact identity", ErrTaskInvalid)
		}
		ids[a.ArtifactID] = true
		if a.DataBase64 == "" {
			continue
		}
		content, err := base64.StdEncoding.DecodeString(a.DataBase64)
		if err != nil || len(content) > 2<<20 {
			return result, nil, fmt.Errorf("%w: invalid artifact payload", ErrTaskInvalid)
		}
		hash := sha256.Sum256(content)
		checksum := hex.EncodeToString(hash[:])
		if (a.SizeBytes != 0 && a.SizeBytes != int64(len(content))) || (a.SHA256 != "" && a.SHA256 != checksum) {
			return result, nil, fmt.Errorf("%w: artifact integrity mismatch", ErrTaskInvalid)
		}
		a.SizeBytes, a.SHA256 = int64(len(content)), checksum
		if a.ExpiresAt.IsZero() {
			a.ExpiresAt = task.CreatedAt.Add(24 * time.Hour)
		}
		if s.artifactStore != nil {
			key := sha256.Sum256([]byte(task.TaskID + "\x00" + a.ArtifactID + "\x00" + checksum))
			a.StorageKey = hex.EncodeToString(key[:])
			a.DataBase64 = ""
			if s.currentTime().Before(a.ExpiresAt) {
				payloads[a.StorageKey] = content
			}
		} else if !s.currentTime().Before(a.ExpiresAt) {
			a.DataBase64 = ""
		}
	}
	return result, payloads, nil
}

// CMS clients keep the bounded inline representation, while snapshots contain
// only metadata. Never modify the stored Task when assembling a response.
func (s *Server) withArtifactPayloads(task protocol.Task) (protocol.Task, error) {
	if task.Result == nil {
		return task, nil
	}
	task = cloneTask(task)
	for i := range task.Result.Artifacts {
		a := &task.Result.Artifacts[i]
		if a.StorageKey == "" || (!a.ExpiresAt.IsZero() && !s.tasks.currentTime().Before(a.ExpiresAt)) {
			continue
		}
		if s.artifactStore == nil {
			return protocol.Task{}, ErrArtifactStorage
		}
		content, err := s.artifactStore.Get(a.StorageKey)
		if err != nil {
			return protocol.Task{}, ErrArtifactStorage
		}
		hash := sha256.Sum256(content)
		if int64(len(content)) != a.SizeBytes || hex.EncodeToString(hash[:]) != a.SHA256 {
			return protocol.Task{}, ErrArtifactStorage
		}
		a.DataBase64 = base64.StdEncoding.EncodeToString(content)
	}
	return task, nil
}
