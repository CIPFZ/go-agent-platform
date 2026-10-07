package gateway

import (
	"errors"
	"net/http"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

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
		status, code := http.StatusNotFound, protocol.ErrorNotFound
		if errors.Is(err, ErrTaskConflict) {
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
	var lease time.Duration
	if body.LeaseSeconds > 0 {
		lease = time.Duration(body.LeaseSeconds) * time.Second
	}
	task, err := s.tasks.Pull(agentID, lease)
	if errors.Is(err, ErrNoTask) {
		writeError(writer, request, http.StatusNoContent, protocol.ErrorNotFound, err.Error(), false)
		return
	}
	if err != nil {
		writeError(writer, request, http.StatusBadRequest, protocol.ErrorInvalidRequest, err.Error(), false)
		return
	}
	s.recordAudit("task.dispatch", agentID, task.TaskID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}

type taskAgentRequest struct {
	protocol.Envelope
	AgentID string `json:"agent_id"`
}

func (s *Server) handleAckTask(writer http.ResponseWriter, request *http.Request, taskID string) {
	var body taskAgentRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := body.Envelope.Validate(); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	task, err := s.tasks.Ack(taskID, body.AgentID)
	if err != nil {
		status, code := http.StatusNotFound, protocol.ErrorNotFound
		if errors.Is(err, ErrTaskConflict) {
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
	task, err := s.tasks.Result(taskID, body.AgentID, body.Result)
	if err != nil {
		status, code := http.StatusNotFound, protocol.ErrorNotFound
		if errors.Is(err, ErrTaskConflict) {
			status, code = http.StatusConflict, protocol.ErrorConflict
		}
		writeError(writer, request, status, code, err.Error(), false)
		return
	}
	s.recordAudit("task.result", body.AgentID, task.TaskID, string(body.Result.Status), nil)
	writeJSON(writer, http.StatusOK, protocol.TaskResponse{Envelope: responseEnvelope(request), Task: task})
}
