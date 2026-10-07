package gateway

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/artifacts"
	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/credentials"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

var (
	ErrNotFound = errors.New("agent not found")
	ErrRevoked  = errors.New("agent is revoked")
)

// Config controls the in-memory gateway state and HTTP server.
type Config struct {
	HeartbeatTimeout     time.Duration
	Now                  func() time.Time
	AuditSink            audit.Sink
	TaskPersistencePath  string
	AgentPersistencePath string
	AuthToken            string
	AuthTokenFile        string
	ArtifactStore        artifacts.Store
	ArtifactDir          string
	ArtifactS3           *artifacts.S3Config
}

func (c Config) heartbeatTimeout() time.Duration {
	if c.HeartbeatTimeout <= 0 {
		return 45 * time.Second
	}
	return c.HeartbeatTimeout
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Store keeps the registered agent identities. It is deliberately in-memory for P1.
type Store struct {
	mu                 sync.RWMutex
	agents             map[string]protocol.Agent
	heartbeatTimeout   time.Duration
	now                func() time.Time
	persistencePath    string
	lastPersistenceErr error
}

func NewStore(config Config) *Store {
	store := &Store{
		agents:           make(map[string]protocol.Agent),
		heartbeatTimeout: config.heartbeatTimeout(),
		now:              config.now,
		persistencePath:  config.AgentPersistencePath,
	}
	if store.persistencePath != "" {
		if err := store.load(); err != nil {
			store.lastPersistenceErr = err
		}
	}
	return store
}

func (s *Store) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) status(agent protocol.Agent, now time.Time) protocol.Agent {
	if agent.RevokedAt != nil {
		agent.Status = protocol.AgentStatusRevoked
	} else if agent.Status == protocol.AgentStatusOffline {
		// A clean shutdown is an explicit state transition. Keep it offline
		// until the agent sends another heartbeat or registers again.
		agent.Status = protocol.AgentStatusOffline
	} else if agent.LastHeartbeat.IsZero() || now.Sub(agent.LastHeartbeat) > s.heartbeatTimeout {
		agent.Status = protocol.AgentStatusOffline
	} else {
		agent.Status = protocol.AgentStatusOnline
	}
	return cloneAgent(agent)
}

func cloneAgent(agent protocol.Agent) protocol.Agent {
	agent.Capabilities = append([]protocol.Capability(nil), agent.Capabilities...)
	for i := range agent.Capabilities {
		agent.Capabilities[i].Attributes = cloneMap(agent.Capabilities[i].Attributes)
	}
	return agent
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (s *Store) Register(request protocol.RegisterRequest) (protocol.Agent, error) {
	if strings.TrimSpace(request.AgentID) == "" {
		return protocol.Agent{}, ErrNotFound
	}
	now := s.currentTime()
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	if existing, ok := s.agents[request.AgentID]; ok {
		if existing.RevokedAt != nil {
			return protocol.Agent{}, ErrRevoked
		}
		existing.AgentVersion = request.AgentVersion
		existing.Hostname = request.Hostname
		existing.Capabilities = cloneCapabilities(request.Capabilities)
		existing.Resource = request.Resource
		existing.LastHeartbeat = now
		existing.LastSeenAt = now
		existing.Status = protocol.AgentStatusOnline
		s.agents[request.AgentID] = existing
		return cloneAgent(existing), nil
	}
	agent := protocol.Agent{
		AgentID:       request.AgentID,
		AgentVersion:  request.AgentVersion,
		Hostname:      request.Hostname,
		Capabilities:  cloneCapabilities(request.Capabilities),
		Status:        protocol.AgentStatusOnline,
		RegisteredAt:  now,
		LastHeartbeat: now,
		LastSeenAt:    now,
		Resource:      request.Resource,
	}
	s.agents[request.AgentID] = agent
	return cloneAgent(agent), nil
}

func cloneCapabilities(input []protocol.Capability) []protocol.Capability {
	output := append([]protocol.Capability(nil), input...)
	for i := range output {
		output[i].Attributes = cloneMap(output[i].Attributes)
	}
	return output
}

func (s *Store) Heartbeat(agentID string, request protocol.HeartbeatRequest) (protocol.Agent, error) {
	now := s.currentTime()
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	agent, ok := s.agents[agentID]
	if !ok {
		return protocol.Agent{}, ErrNotFound
	}
	if agent.RevokedAt != nil {
		return protocol.Agent{}, ErrRevoked
	}
	if request.AgentVersion != "" {
		agent.AgentVersion = request.AgentVersion
	}
	if request.Capabilities != nil {
		agent.Capabilities = cloneCapabilities(request.Capabilities)
	}
	agent.Resource = request.Resource
	agent.LastHeartbeat = now
	agent.LastSeenAt = now
	agent.Status = protocol.AgentStatusOnline
	s.agents[agentID] = agent
	return cloneAgent(agent), nil
}

func (s *Store) Get(agentID string) (protocol.Agent, error) {
	now := s.currentTime()
	s.mu.RLock()
	defer s.mu.RUnlock()
	agent, ok := s.agents[agentID]
	if !ok {
		return protocol.Agent{}, ErrNotFound
	}
	return s.status(agent, now), nil
}

func (s *Store) List() []protocol.Agent {
	now := s.currentTime()
	s.mu.RLock()
	defer s.mu.RUnlock()
	output := make([]protocol.Agent, 0, len(s.agents))
	for _, agent := range s.agents {
		output = append(output, s.status(agent, now))
	}
	sort.Slice(output, func(i, j int) bool { return output[i].AgentID < output[j].AgentID })
	return output
}

type agentSnapshot struct {
	Agents map[string]protocol.Agent `json:"agents"`
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.persistencePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot agentSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	if snapshot.Agents != nil {
		s.agents = snapshot.Agents
	}
	return nil
}

func (s *Store) persistLocked() {
	if s.persistencePath == "" {
		return
	}
	data, err := json.Marshal(agentSnapshot{Agents: s.agents})
	if err == nil {
		dir := filepath.Dir(s.persistencePath)
		if err = os.MkdirAll(dir, 0o700); err == nil {
			file, createErr := os.CreateTemp(dir, "agents-*.tmp")
			if createErr != nil {
				err = createErr
			} else {
				name := file.Name()
				defer os.Remove(name)
				if _, err = file.Write(data); err == nil {
					err = file.Chmod(0o600)
				}
				if closeErr := file.Close(); err == nil {
					err = closeErr
				}
				if err == nil {
					err = os.Rename(name, s.persistencePath)
				}
			}
		}
	}
	s.lastPersistenceErr = err
}

func (s *Store) PersistenceError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastPersistenceErr
}

func (s *Store) Revoke(agentID string) (protocol.Agent, error) {
	now := s.currentTime()
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	agent, ok := s.agents[agentID]
	if !ok {
		return protocol.Agent{}, ErrNotFound
	}
	if agent.RevokedAt == nil {
		agent.RevokedAt = &now
	}
	agent.Status = protocol.AgentStatusRevoked
	s.agents[agentID] = agent
	return cloneAgent(agent), nil
}

// Offline records a clean Agent shutdown while keeping its identity registered.
func (s *Store) Offline(agentID string) (protocol.Agent, error) {
	s.mu.Lock()
	defer func() { s.persistLocked(); s.mu.Unlock() }()
	agent, ok := s.agents[agentID]
	if !ok {
		return protocol.Agent{}, ErrNotFound
	}
	if agent.RevokedAt != nil {
		return protocol.Agent{}, ErrRevoked
	}
	agent.Status = protocol.AgentStatusOffline
	s.agents[agentID] = agent
	return cloneAgent(agent), nil
}

// Server implements the versioned Gateway HTTP API.
type Server struct {
	store            *Store
	tasks            *TaskStore
	auditSink        audit.Sink
	artifactStore    artifacts.Store
	artifactStoreErr error
	authToken        string
	authTokenFile    string
	requests         atomic.Uint64
}

func NewServer(config Config) *Server {
	server := &Server{store: NewStore(config), tasks: NewTaskStore(TaskConfig{Now: config.Now, PersistencePath: config.TaskPersistencePath}), auditSink: config.AuditSink, authToken: config.AuthToken, authTokenFile: config.AuthTokenFile, artifactStore: config.ArtifactStore}
	if server.artifactStore == nil && config.ArtifactDir != "" {
		server.artifactStore, server.artifactStoreErr = artifacts.NewFileStore(config.ArtifactDir)
	}
	if server.artifactStore == nil && config.ArtifactS3 != nil {
		server.artifactStore, server.artifactStoreErr = artifacts.NewS3Store(*config.ArtifactS3)
	}
	server.tasks.artifactStore = server.artifactStore
	return server
}

func (s *Server) Store() *Store { return s.store }

func (s *Server) Tasks() *TaskStore { return s.tasks }

func (s *Server) Handler() http.Handler { return s }

func (s *Server) recordAudit(kind, actor, resource, outcome string, details map[string]string) {
	if s.auditSink != nil {
		s.auditSink.Record(audit.Event{Time: time.Now().UTC(), Kind: kind, Actor: actor, ResourceID: resource, Outcome: outcome, Details: details})
	}
}

func (s *Server) artifactStorageReady() error {
	if s.artifactStoreErr != nil {
		return s.artifactStoreErr
	}
	if checker, ok := s.artifactStore.(interface{ Health() error }); ok {
		return checker.Health()
	}
	return nil
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.requests.Add(1)
	if strings.HasPrefix(request.URL.Path, "/api/") {
		expected, err := s.configuredAuthToken()
		if err != nil {
			writeError(writer, request, http.StatusServiceUnavailable, protocol.ErrorInternal, "authentication credential unavailable", true)
			return
		}
		if expected != "" && subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+expected)) != 1 {
			writeError(writer, request, http.StatusUnauthorized, protocol.ErrorUnauthorized, "authorization required", false)
			return
		}
	}
	if request.URL.Path == "/metrics" {
		if request.Method != http.MethodGet {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handleMetrics(writer)
		return
	}
	if request.URL.Path == "/healthz" {
		if request.Method != http.MethodGet {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		agents := s.store.List()
		online := 0
		for _, agent := range agents {
			if agent.Status == protocol.AgentStatusOnline {
				online++
			}
		}
		status := "ok"
		statusCode := http.StatusOK
		_, authErr := s.configuredAuthToken()
		tasks := s.tasks.List()
		var auditErr error
		if reporter, ok := s.auditSink.(interface{ LastError() error }); ok {
			auditErr = reporter.LastError()
		}
		if authErr != nil || auditErr != nil || s.artifactStoreErr != nil || s.store.PersistenceError() != nil || s.tasks.PersistenceError() != nil {
			status = "degraded"
			statusCode = http.StatusServiceUnavailable
		}
		health := map[string]any{"protocol_version": protocol.Version, "request_id": responseEnvelope(request).RequestID, "timestamp": time.Now().UTC(), "status": status, "service": "gateway", "agents": len(agents), "agents_online": online, "tasks": len(tasks)}
		writeJSON(writer, statusCode, health)
		return
	}
	if request.URL.Path == "/api/v1/agents" {
		if request.Method != http.MethodGet {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handleList(writer, request)
		return
	}
	if request.URL.Path == "/api/v1/agents/register" {
		if request.Method != http.MethodPost {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handleRegister(writer, request)
		return
	}
	if request.URL.Path == "/api/v1/tasks" {
		if request.Method == http.MethodPost {
			s.handleCreateTask(writer, request)
			return
		}
		if request.Method == http.MethodGet {
			s.handleListTasks(writer, request)
			return
		}
		writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
		return
	}
	const taskPrefix = "/api/v1/tasks/"
	if strings.HasPrefix(request.URL.Path, taskPrefix) {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(request.URL.Path, taskPrefix), "/"), "/")
		if len(parts) == 1 && parts[0] != "" {
			if request.Method == http.MethodGet {
				s.handleGetTask(writer, request, parts[0])
				return
			}
			if request.Method == http.MethodPost {
				s.handleCancelTask(writer, request, parts[0])
				return
			}
		}
		if len(parts) == 2 && parts[0] != "" {
			switch parts[1] {
			case "renew":
				if request.Method == http.MethodPost {
					s.handleRenewTask(writer, request, parts[0])
					return
				}
			case "cancel":
				if request.Method == http.MethodPost {
					s.handleCancelTask(writer, request, parts[0])
					return
				}
			case "ack":
				if request.Method == http.MethodPost {
					s.handleAckTask(writer, request, parts[0])
					return
				}
			case "result":
				if request.Method == http.MethodPost {
					s.handleTaskResult(writer, request, parts[0])
					return
				}
			}
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "artifacts" && parts[2] != "" {
			if request.Method == http.MethodGet {
				s.handleArtifactDownload(writer, request, parts[0], parts[2])
				return
			}
		}
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "endpoint not found", false)
		return
	}
	const prefix = "/api/v1/agents/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "endpoint not found", false)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(request.URL.Path, prefix), "/"), "/")
	if len(parts) == 3 && parts[0] != "" && parts[1] == "tasks" && parts[2] == "pull" {
		if request.Method != http.MethodPost {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handlePullTask(writer, request, parts[0])
		return
	}
	if len(parts) == 1 && parts[0] != "" {
		if request.Method != http.MethodGet {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handleGet(writer, request, parts[0])
		return
	}
	if len(parts) != 2 || parts[0] == "" {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "endpoint not found", false)
		return
	}
	agentID, action := parts[0], parts[1]
	switch action {
	case "heartbeat":
		if request.Method != http.MethodPost {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handleHeartbeat(writer, request, agentID)
	case "revoke":
		if request.Method != http.MethodPost && request.Method != http.MethodDelete {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handleRevoke(writer, request, agentID)
	case "offline":
		if request.Method != http.MethodPost {
			writeError(writer, request, http.StatusMethodNotAllowed, protocol.ErrorInvalidRequest, "method not allowed", false)
			return
		}
		s.handleOffline(writer, request, agentID)
	default:
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "endpoint not found", false)
	}
}

func (s *Server) configuredAuthToken() (string, error) {
	return credentials.Resolve(s.authToken, s.authTokenFile)
}

func (s *Server) handleRegister(writer http.ResponseWriter, request *http.Request) {
	var body protocol.RegisterRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := validateEnvelope(body.Envelope); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	if strings.TrimSpace(body.AgentID) == "" || strings.TrimSpace(body.AgentVersion) == "" {
		writeError(writer, request, http.StatusBadRequest, protocol.ErrorInvalidRequest, "agent_id and agent_version are required", false)
		return
	}
	agent, err := s.store.Register(body)
	if err != nil {
		if errors.Is(err, ErrRevoked) {
			writeError(writer, request, http.StatusForbidden, protocol.ErrorRevoked, "agent has been revoked", false)
			return
		}
		writeError(writer, request, http.StatusBadRequest, protocol.ErrorInvalidRequest, err.Error(), false)
		return
	}
	s.recordAudit("agent.register", body.AgentID, body.AgentID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.RegisterResponse{Envelope: responseEnvelope(request), Agent: agent})
}

func (s *Server) handleHeartbeat(writer http.ResponseWriter, request *http.Request, agentID string) {
	var body protocol.HeartbeatRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := validateEnvelope(body.Envelope); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	agent, err := s.store.Heartbeat(agentID, body)
	if err != nil {
		status, code, message := http.StatusNotFound, protocol.ErrorNotFound, "agent not found"
		if errors.Is(err, ErrRevoked) {
			status, code, message = http.StatusForbidden, protocol.ErrorRevoked, "agent has been revoked"
		}
		writeError(writer, request, status, code, message, false)
		return
	}
	s.recordAudit("agent.heartbeat", agentID, agentID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.HeartbeatResponse{Envelope: responseEnvelope(request), Agent: agent})
}

func (s *Server) handleList(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, protocol.AgentsResponse{Envelope: responseEnvelope(request), Agents: s.store.List()})
}

func (s *Server) handleGet(writer http.ResponseWriter, request *http.Request, agentID string) {
	agent, err := s.store.Get(agentID)
	if errors.Is(err, ErrNotFound) {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "agent not found", false)
		return
	}
	writeJSON(writer, http.StatusOK, protocol.AgentResponse{Envelope: responseEnvelope(request), Agent: agent})
}

func (s *Server) handleRevoke(writer http.ResponseWriter, request *http.Request, agentID string) {
	agent, err := s.store.Revoke(agentID)
	if errors.Is(err, ErrNotFound) {
		writeError(writer, request, http.StatusNotFound, protocol.ErrorNotFound, "agent not found", false)
		return
	}
	s.recordAudit("agent.revoke", agentID, agentID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.AgentResponse{Envelope: responseEnvelope(request), Agent: agent})
}

func (s *Server) handleOffline(writer http.ResponseWriter, request *http.Request, agentID string) {
	var body protocol.Envelope
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := validateEnvelope(body); err != nil {
		writeEnvelopeError(writer, request, err)
		return
	}
	agent, err := s.store.Offline(agentID)
	if err != nil {
		status, code, message := http.StatusNotFound, protocol.ErrorNotFound, "agent not found"
		if errors.Is(err, ErrRevoked) {
			status, code, message = http.StatusForbidden, protocol.ErrorRevoked, "agent has been revoked"
		}
		writeError(writer, request, status, code, message, false)
		return
	}
	s.recordAudit("agent.offline", agentID, agentID, "accepted", nil)
	writeJSON(writer, http.StatusOK, protocol.AgentResponse{Envelope: responseEnvelope(request), Agent: agent})
}

func validateEnvelope(envelope protocol.Envelope) error {
	return envelope.Validate()
}

func responseEnvelope(request *http.Request) protocol.Envelope {
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = newRequestID()
	}
	return protocol.Envelope{ProtocolVersion: protocol.Version, RequestID: requestID, Timestamp: time.Now().UTC()}
}

func writeError(writer http.ResponseWriter, request *http.Request, status int, code protocol.ErrorCode, message string, retryable bool) {
	writeJSON(writer, status, protocol.ErrorResponse{Envelope: responseEnvelope(request), Error: protocol.APIError{Code: code, Message: message, Retryable: retryable}})
}

func writeEnvelopeError(writer http.ResponseWriter, request *http.Request, err error) {
	code := protocol.ErrorInvalidRequest
	if errors.Is(err, protocol.ErrUnsupportedVersion) {
		code = protocol.ErrorUnsupportedVersion
	}
	writeError(writer, request, http.StatusBadRequest, code, err.Error(), false)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (s *Server) handleMetrics(writer http.ResponseWriter) {
	agents := s.store.List()
	online := 0
	for _, agent := range agents {
		if agent.Status == protocol.AgentStatusOnline {
			online++
		}
	}
	tasks := s.tasks.List()
	queued, running, failed := 0, 0, 0
	for _, task := range tasks {
		switch task.Status {
		case protocol.TaskStatusQueued, protocol.TaskStatusDispatched:
			queued++
		case protocol.TaskStatusRunning:
			running++
		case protocol.TaskStatusFailed, protocol.TaskStatusExpired:
			failed++
		}
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	writer.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(writer, "gateway_requests_total %d\nagent_online %d\nagent_registered %d\ntask_queued %d\ntask_running %d\ntask_failed %d\n", s.requests.Load(), online, len(agents), queued, running, failed)
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, value any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 4<<20)
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(value); err != nil {
		writeError(writer, request, http.StatusBadRequest, protocol.ErrorInvalidRequest, "invalid JSON body", false)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(writer, request, http.StatusBadRequest, protocol.ErrorInvalidRequest, "request body must contain one JSON object", false)
		return false
	}
	return true
}

func newRequestID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "request"
	}
	return hex.EncodeToString(buffer)
}
