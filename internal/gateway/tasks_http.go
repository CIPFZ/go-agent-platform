package gateway

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func (s *Server) handleArtifactDownload(writer http.ResponseWriter, request *http.Request, taskID, artifactID string) {
	task, err := s.tasks.Get(taskID)
	if errors.Is(err, ErrTaskNotFound) {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "task not found", false)
		return
	}
	if err != nil || task.Result == nil {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "artifact not found", false)
		return
	}
	for _, artifact := range task.Result.Artifacts {
		if artifact.ArtifactID != artifactID {
			continue
		}
		if artifact.DataBase64 == "" {
			if !artifact.ExpiresAt.IsZero() && !time.Now().UTC().Before(artifact.ExpiresAt) {
				writeError(writer, request, http.StatusGone, protocol.ErrorNotFound, "artifact expired", false)
				return
			}
			writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "artifact payload unavailable", false)
			return
		}
		content, err := base64.StdEncoding.DecodeString(artifact.DataBase64)
		if err != nil {
			writeError(writer, request, http.StatusInternalServerError, protocol.ErrorInternal, "artifact payload invalid", false)
			return
		}
		if artifact.SizeBytes != int64(len(content)) {
			writeError(writer, request, http.StatusInternalServerError, protocol.ErrorInternal, "artifact size mismatch", false)
			return
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		if artifact.MediaType != "" {
			writer.Header().Set("Content-Type", artifact.MediaType)
		}
		writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		writer.Header().Set("ETag", `"`+artifact.SHA256+`"`)
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = writer.Write(content)
		return
	}
	writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "artifact not found", false)
}

func (s *Server) handleCreateTask(writer http.ResponseWriter, request *http.Request) {
	var body protocol.CreateTaskRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := body.Envelope.Validate(); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	task, err := s.tasks.Create(body)
	if err != nil {
		status, code := http.StatusBadRequest, protocol.ErrorInvalidRequest
		if errors.Is(err, ErrIdempotencyConflict) {
			status, code = http.StatusConflict, protocol.ErrorConflict
		}
		if errors.Is(err, ErrQueueFull) {
			status, code = http.StatusTooManyRequests, protocol.ErrorConflict
		}
		writeError(writer, request, status, code, err.Error(), errors.Is(err, ErrQueueFull))
		return
	}
	s.recordAudit("task.create", body.CallerID, task.TaskID, "accepted", map[string]string{"action": string(body.Action.Type)})
	writeJSON(writer, http.StatusCreated, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}

func (s *Server) handleListTasks(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, protocol.TasksResponse{Envelope: responseEnvelope(request), Tasks: s.tasks.List()})
}

func (s *Server) handleGetTask(writer http.ResponseWriter, request *http.Request, taskID string) {
	task, err := s.tasks.Get(taskID)
	if errors.Is(err, ErrTaskNotFound) {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, err.Error(), false)
		return
	}
	writeJSON(writer, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}

func (s *Server) handleCancelTask(writer http.ResponseWriter, request *http.Request, taskID string) {
	var body protocol.Envelope
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := body.Validate(); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	task, err := s.tasks.Cancel(taskID)
	if err != nil {
		status, code := http.StatusBadRequest, protocol.ErrorInvalidRequest
		if errors.Is(err, ErrTaskNotFound) {
			status, code = http.StatusNotFound, protocol.ErrorNotFound
		}
		if errors.Is(err, ErrTaskConflict) || errors.Is(err, ErrTaskLease) || errors.Is(err, ErrIdempotencyConflict) {
			status, code = http.StatusConflict, protocol.ErrorConflict
		}
		writeError(writer, request, status, code, err.Error(), false)
		return
	}
	s.recordAudit("task.cancel", "", task.TaskID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}

func (s *Server) handlePullTask(writer http.ResponseWriter, request *http.Request, agentID string) {
	var body protocol.PullTasksRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := body.Envelope.Validate(); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	agent, err := s.store.Get(agentID)
	if errors.Is(err, ErrNotFound) {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "agent not found", false)
		return
	}
	if agent.Status == protocol.AgentStatusRevoked {
		writeError(writer, request, http.StatusForbidden, protocol.ErrorRevoked, "agent has been revoked", false)
		return
	}
	if body.LeaseSeconds < 0 || body.LeaseSeconds > 300 {
		writeError(writer, request, http.StatusBadRequest, protocol.ErrorInvalidRequest, "lease_seconds must be 0..300", false)
		return
	}
	var lease time.Duration
	if body.LeaseSeconds > 0 {
		lease = time.Duration(body.LeaseSeconds) * time.Second
	}
	task, err := s.tasks.Pull(agentID, lease)
	if errors.Is(err, ErrNoTask) {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, protocol.ErrorInvalidRequest, err.Error(), false)
		return
	}
	s.recordAudit("task.dispatch", agentID, task.TaskID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}

type taskAgentRequest = protocol.TaskLeaseRequest

func (s *Server) handleAckTask(writer http.ResponseWriter, request *http.Request, taskID string) {
	var body taskAgentRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := body.Envelope.Validate(); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	if !s.requireActiveAgent(writer, request, body.AgentID) {
		return
	}
	task, err := s.tasks.Ack(taskID, body.AgentID, body.LeaseToken)
	if err != nil {
		status, code := http.StatusBadRequest, protocol.ErrorInvalidRequest
		if errors.Is(err, ErrTaskNotFound) {
			status, code = http.StatusNotFound, protocol.ErrorNotFound
		}
		if errors.Is(err, ErrTaskConflict) || errors.Is(err, ErrTaskLease) || errors.Is(err, ErrIdempotencyConflict) {
			status, code = http.StatusConflict, protocol.ErrorConflict
		}
		writeError(writer, request, status, code, err.Error(), false)
		return
	}
	s.recordAudit("task.ack", body.AgentID, task.TaskID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}

func (s *Server) handleTaskResult(writer http.ResponseWriter, request *http.Request, taskID string) {
	var body protocol.TaskResultRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := body.Envelope.Validate(); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	if !s.requireActiveAgent(writer, request, body.AgentID) {
		return
	}
	task, err := s.tasks.Result(taskID, body.AgentID, body.LeaseToken, body.Result)
	if err != nil {
		status, code := http.StatusBadRequest, protocol.ErrorInvalidRequest
		if errors.Is(err, ErrTaskNotFound) {
			status, code = http.StatusNotFound, protocol.ErrorNotFound
		}
		if errors.Is(err, ErrTaskConflict) || errors.Is(err, ErrTaskLease) || errors.Is(err, ErrIdempotencyConflict) {
			status, code = http.StatusConflict, protocol.ErrorConflict
		}
		writeError(writer, request, status, code, err.Error(), false)
		return
	}
	s.recordAudit("task.result", body.AgentID, task.TaskID, string(body.Result.Status), nil)
	writeJSON(writer, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}

func (s *Server) handleRenewTask(w http.ResponseWriter, r *http.Request, id string) {
	var body protocol.TaskLeaseRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := body.Envelope.Validate(); err != nil {
		writeEnvelopeError(w, r, err)
		return
	}
	if !s.requireActiveAgent(w, r, body.AgentID) {
		return
	}
	task, err := s.tasks.Renew(id, body.AgentID, body.LeaseToken)
	if err != nil {
		status, code := http.StatusConflict, protocol.ErrorConflict
		if errors.Is(err, ErrTaskNotFound) {
			status, code = http.StatusNotFound, protocol.ErrorNotFound
		}
		writeError(w, r, status, code, err.Error(), false)
		return
	}
	writeJSON(w, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(r), Task: task})
}
func (s *Server) requireActiveAgent(w http.ResponseWriter, r *http.Request, id string) bool {
	a, err := s.store.Get(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, protocol.ErrorNotFound, "agent not found", false)
		return false
	}
	if a.Status == protocol.AgentStatusRevoked {
		writeError(w, r, http.StatusForbidden, protocol.ErrorRevoked, "agent has been revoked", false)
		return false
	}
	return true
}
