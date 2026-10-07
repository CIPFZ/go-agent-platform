package cms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Client is the narrow HTTP boundary a CMS integration can use. It keeps CMS
// independent from Gateway internals and leaves VM lifecycle ownership there.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (c Client) CreateTask(ctx context.Context, request protocol.CreateTaskRequest) (protocol.Task, error) {
	var response protocol.TaskResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/tasks", request, &response); err != nil {
		return protocol.Task{}, err
	}
	return response.Task, nil
}

func (c Client) GetTask(ctx context.Context, taskID string) (protocol.Task, error) {
	var response protocol.TaskResponse
	if err := c.do(ctx, http.MethodGet, "/api/v1/tasks/"+taskID, nil, &response); err != nil {
		return protocol.Task{}, err
	}
	return response.Task, nil
}

func (c Client) CancelTask(ctx context.Context, taskID string) (protocol.Task, error) {
	request := protocol.Envelope{ProtocolVersion: protocol.Version, RequestID: fmt.Sprintf("cms-%d", time.Now().UnixNano()), Timestamp: time.Now().UTC()}
	var response protocol.TaskResponse
	if err := c.do(ctx, http.MethodPost, "/api/v1/tasks/"+taskID, request, &response); err != nil {
		return protocol.Task{}, err
	}
	return response.Task, nil
}

func (c Client) do(ctx context.Context, method, path string, body any, result any) error {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return errors.New("gateway base URL is required")
	}
	payload := []byte("null")
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("gateway returned HTTP %s", response.Status)
	}
	if result != nil && response.StatusCode != http.StatusNoContent {
		return json.NewDecoder(response.Body).Decode(result)
	}
	return nil
}
