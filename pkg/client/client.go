// Package client is the Go client of the vm-agent HTTP API. The endpoint
// methods and types in client_gen.go are generated from docs/openapi.yaml
// by tools/clientgen; this file holds the hand-written transport.
//
//	c := client.New("http://10.0.0.5:7070")
//	shot, err := c.ScreenCapture(ctx, &client.CaptureRequest{Cursor: client.Ptr(true)})
//	err = c.MouseClick(ctx, &client.MouseClickRequest{X: client.Ptr(100), Y: client.Ptr(200)},
//		client.WithRequestID("click-0001-abcdef01"))
//
// Errors returned by the service are *Error values carrying the code of
// docs/errors.md.
package client

//go:generate sh -c "cd ../../tools && go run ./clientgen -spec ../docs/openapi.yaml -out ../pkg/client/client_gen.go"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client calls one vm-agent.
type Client struct {
	// BaseURL is the agent root, e.g. http://10.0.0.5:7070.
	BaseURL string
	// HTTP is the transport; nil means http.DefaultClient. Leave its Timeout
	// unset and use WithTimeout or the context instead, so streaming
	// downloads are not cut off.
	HTTP *http.Client
}

// New returns a client for baseURL.
func New(baseURL string) *Client { return &Client{BaseURL: strings.TrimRight(baseURL, "/")} }

// Ptr returns a pointer to v, for optional request fields.
func Ptr[T any](v T) *T { return &v }

// Option adjusts one call.
type Option func(*callOptions)

type callOptions struct {
	requestID string
	timeout   time.Duration
	info      *ResponseInfo
	header    http.Header
}

// WithRequestID sets X-Request-Id (16 to 128 characters of
// [A-Za-z0-9._-]). Non-retryable actions are deduplicated by it.
func WithRequestID(id string) Option { return func(o *callOptions) { o.requestID = id } }

// WithTimeout sets X-Timeout-Ms, the server-side action timeout.
func WithTimeout(d time.Duration) Option { return func(o *callOptions) { o.timeout = d } }

// WithHeader adds a request header, e.g. for an authentication proxy in front
// of the agent.
func WithHeader(key, value string) Option {
	return func(o *callOptions) {
		if o.header == nil {
			o.header = http.Header{}
		}
		o.header.Add(key, value)
	}
}

// ResponseInfo receives envelope metadata when passed with WithResponseInfo.
type ResponseInfo struct {
	RequestID    string
	ElapsedMS    int64
	DedupHit     bool
	AgentVersion string
	HTTPStatus   int
}

// WithResponseInfo captures the response metadata into info.
func WithResponseInfo(info *ResponseInfo) Option { return func(o *callOptions) { o.info = info } }

// Error is an error response of the service (docs/errors.md).
type Error struct {
	// HTTPStatus is the response status; a replayed error (X-Dedup-Hit) has 200.
	HTTPStatus int
	RequestID  string
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	Retryable  bool           `json:"retryable"`
	Details    map[string]any `json:"details"`
}

func (e *Error) Error() string { return fmt.Sprintf("vm-agent %s: %s", e.Code, e.Message) }

// Reason returns details.reason, or "".
func (e *Error) Reason() string {
	r, _ := e.Details["reason"].(string)
	return r
}

// StreamResponse is a raw-body response; the caller must close Body.
type StreamResponse struct {
	ContentType string
	// Size is X-Result-Size (or Content-Length), -1 when unknown.
	Size   int64
	Header http.Header
	Body   io.ReadCloser
}

// Close closes the body.
func (s *StreamResponse) Close() error { return s.Body.Close() }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, opts []Option) (*http.Request, *callOptions, error) {
	o := &callOptions{}
	for _, opt := range opts {
		opt(o)
	}
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if o.requestID != "" {
		req.Header.Set("X-Request-Id", o.requestID)
	}
	if o.timeout > 0 {
		req.Header.Set("X-Timeout-Ms", strconv.FormatInt(max(o.timeout.Milliseconds(), 1), 10))
	}
	for k, vs := range o.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req, o, nil
}

type envelope struct {
	OK        bool            `json:"ok"`
	RequestID string          `json:"request_id"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Result    json.RawMessage `json:"result"`
	Error     *Error          `json:"error"`
}

func record(o *callOptions, resp *http.Response, env *envelope) {
	if o.info == nil {
		return
	}
	o.info.HTTPStatus = resp.StatusCode
	o.info.DedupHit = resp.Header.Get("X-Dedup-Hit") == "true"
	o.info.AgentVersion = resp.Header.Get("X-Agent-Version")
	if env != nil {
		o.info.RequestID, o.info.ElapsedMS = env.RequestID, env.ElapsedMS
	}
}

func decodeEnvelope(resp *http.Response, o *callOptions, out any) error {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("vm-agent: HTTP %d with a non-envelope body: %.200q", resp.StatusCode, data)
	}
	record(o, resp, &env)
	if !env.OK {
		if env.Error == nil {
			env.Error = &Error{Code: "internal", Message: "error response without error object"}
		}
		env.Error.HTTPStatus, env.Error.RequestID = resp.StatusCode, env.RequestID
		return env.Error
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any, opts []Option) error {
	var rd io.Reader
	ct := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd, ct = bytes.NewReader(data), "application/json"
	}
	req, o, err := c.newRequest(ctx, method, path, query, rd, ct, opts)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeEnvelope(resp, o, out)
}

func (c *Client) doStream(ctx context.Context, method, path string, query url.Values, body any, opts []Option) (*StreamResponse, error) {
	var rd io.Reader
	ct := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd, ct = bytes.NewReader(data), "application/json"
	}
	req, o, err := c.newRequest(ctx, method, path, query, rd, ct, opts)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		// Errors (and replays) always come back as an envelope.
		defer resp.Body.Close()
		if err := decodeEnvelope(resp, o, nil); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("vm-agent: expected a stream, got a JSON result")
	}
	record(o, resp, nil)
	size := resp.ContentLength
	if v, err := strconv.ParseInt(resp.Header.Get("X-Result-Size"), 10, 64); err == nil {
		size = v
	}
	return &StreamResponse{ContentType: resp.Header.Get("Content-Type"), Size: size, Header: resp.Header, Body: resp.Body}, nil
}
