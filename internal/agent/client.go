package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

const (
	defaultHeartbeatInterval = 15 * time.Second
	defaultRequestTimeout    = 10 * time.Second
)

var ErrNoTask = errors.New("no task available")

// Config contains the stable identity and transport settings for one guest Agent.
type Config struct {
	GatewayURL        string
	AgentID           string
	AgentVersion      string
	Hostname          string
	Capabilities      []protocol.Capability
	Resource          protocol.ResourceSummary
	HeartbeatInterval time.Duration
	RequestTimeout    time.Duration
	HTTPClient        *http.Client
	Now               func() time.Time
}

// Client registers one Agent and maintains its heartbeat until its context ends.
type Client struct {
	config     Config
	httpClient *http.Client
	now        func() time.Time
}

func NewClient(config Config) (*Client, error) {
	config.GatewayURL = strings.TrimRight(strings.TrimSpace(config.GatewayURL), "/")
	if config.GatewayURL == "" {
		return nil, errors.New("gateway URL is required")
	}
	if strings.TrimSpace(config.AgentID) == "" {
		return nil, errors.New("agent ID is required")
	}
	if strings.TrimSpace(config.AgentVersion) == "" {
		return nil, errors.New("agent version is required")
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRequestTimeout
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: config.RequestTimeout}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Client{config: config, httpClient: client, now: now}, nil
}

func (c *Client) Register(ctx context.Context) (protocol.Agent, error) {
	request := protocol.RegisterRequest{
		Envelope:     c.envelope(),
		AgentID:      c.config.AgentID,
		AgentVersion: c.config.AgentVersion,
		Hostname:     c.config.Hostname,
		Capabilities: cloneCapabilities(c.config.Capabilities),
		Resource:     c.config.Resource,
	}
	var response protocol.RegisterResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agents/register", request, &response); err != nil {
		return protocol.Agent{}, err
	}
	return response.Agent, nil
}

func (c *Client) Heartbeat(ctx context.Context) (protocol.Agent, error) {
	request := protocol.HeartbeatRequest{
		Envelope:     c.envelope(),
		AgentVersion: c.config.AgentVersion,
		Capabilities: cloneCapabilities(c.config.Capabilities),
		Resource:     c.config.Resource,
	}
	var response protocol.HeartbeatResponse
	path := fmt.Sprintf("/api/v1/agents/%s/heartbeat", c.config.AgentID)
	if err := c.doJSON(ctx, http.MethodPost, path, request, &response); err != nil {
		return protocol.Agent{}, err
	}
	return response.Agent, nil
}

// Offline asks the Gateway to mark this Agent offline while keeping its identity.
func (c *Client) Offline(ctx context.Context) (protocol.Agent, error) {
	var response protocol.AgentResponse
	path := fmt.Sprintf("/api/v1/agents/%s/offline", c.config.AgentID)
	if err := c.doJSON(ctx, http.MethodPost, path, c.envelope(), &response); err != nil {
		return protocol.Agent{}, err
	}
	return response.Agent, nil
}

func (c *Client) PullTask(ctx context.Context, leaseSeconds int64) (protocol.Task, error) {
	request := protocol.PullTasksRequest{Envelope: c.envelope(), LeaseSeconds: leaseSeconds}
	var response protocol.TaskResponse
	path := fmt.Sprintf("/api/v1/agents/%s/tasks/pull", c.config.AgentID)
	if err := c.doJSON(ctx, http.MethodPost, path, request, &response); err != nil {
		if errors.Is(err, errNoContent) {
			return protocol.Task{}, ErrNoTask
		}
		return protocol.Task{}, err
	}
	if response.Task.TaskID == "" {
		return protocol.Task{}, ErrNoTask
	}
	return response.Task, nil
}

func (c *Client) AckTask(ctx context.Context, taskID string) (protocol.Task, error) {
	request := struct {
		protocol.Envelope
		AgentID string `json:"agent_id"`
	}{Envelope: c.envelope(), AgentID: c.config.AgentID}
	var response protocol.TaskResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/ack", request, &response); err != nil {
		return protocol.Task{}, err
	}
	return response.Task, nil
}

func (c *Client) SubmitTaskResult(ctx context.Context, taskID string, result protocol.TaskResult) (protocol.Task, error) {
	request := protocol.TaskResultRequest{Envelope: c.envelope(), AgentID: c.config.AgentID, Result: result}
	var response protocol.TaskResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+taskID+"/result", request, &response); err != nil {
		return protocol.Task{}, err
	}
	return response.Task, nil
}

// Run registers once, sends heartbeats, and attempts a best-effort clean shutdown.
func (c *Client) Run(ctx context.Context) error {
	if _, err := c.Register(ctx); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), c.config.RequestTimeout)
		_, _ = c.Offline(shutdownCtx)
		cancel()
	}()
	ticker := time.NewTicker(c.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := c.Heartbeat(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
		}
	}
}

func (c *Client) envelope() protocol.Envelope {
	return protocol.Envelope{ProtocolVersion: protocol.Version, RequestID: newRequestID(), Timestamp: c.now().UTC()}
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestBody any, responseBody any) error {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.config.GatewayURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("gateway request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return errNoContent
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var apiError protocol.ErrorResponse
		if decodeErr := json.NewDecoder(response.Body).Decode(&apiError); decodeErr == nil && apiError.Error.Message != "" {
			return fmt.Errorf("gateway returned %s: %s", apiError.Error.Code, apiError.Error.Message)
		}
		return fmt.Errorf("gateway returned HTTP %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(responseBody); err != nil {
		return fmt.Errorf("decode gateway response: %w", err)
	}
	return nil
}

var errNoContent = errors.New("gateway returned no content")

func cloneCapabilities(input []protocol.Capability) []protocol.Capability {
	output := append([]protocol.Capability(nil), input...)
	for i := range output {
		if input[i].Attributes == nil {
			continue
		}
		output[i].Attributes = make(map[string]string, len(input[i].Attributes))
		for key, value := range input[i].Attributes {
			output[i].Attributes[key] = value
		}
	}
	return output
}

func newRequestID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}
	return "agent-" + hex.EncodeToString(buffer)
}
