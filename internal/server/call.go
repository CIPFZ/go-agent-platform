package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Call is the context of one request passed to a handler.
type Call struct {
	Request *http.Request
	Writer  http.ResponseWriter
	route   *Route
	info    *info
	server  *Server
	cancels []context.CancelFunc
}

// Stream is a raw-body result (spec D5, section 5.4): the body is written
// as-is with metadata in response headers. Closers are closed after writing.
type Stream struct {
	ContentType string
	Size        int64 // -1 if unknown
	Header      map[string]string
	Body        io.Reader
}

// RequestID is the caller's X-Request-Id or a generated one.
func (c *Call) RequestID() string { return c.info.requestID }

// Param records an audit parameter summary entry (spec section 8).
func (c *Call) Param(key, value string) { c.info.params[key] = value }

// Verbose reports whether -audit-verbose is set.
func (c *Call) Verbose() bool { return c.server.opts.AuditVerbose }

// Decode reads a JSON request body (at most 8 MiB) into v. An empty body
// decodes as {}. Unknown fields are rejected so typos do not silently fall
// back to defaults.
func (c *Call) Decode(v any) error {
	body := http.MaxBytesReader(c.Writer, c.Request.Body, JSONBodyMax)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return protocol.Errorf(protocol.CodeTooLarge, "JSON request body exceeds %d bytes", JSONBodyMax)
		}
		return protocol.Errorf(protocol.CodeInvalidRequest, "read request body: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid JSON body: %v", err).WithReason(protocol.ReasonInvalidJSON)
	}
	if dec.More() {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid JSON body: trailing data").WithReason(protocol.ReasonInvalidJSON)
	}
	return nil
}

// Timeout returns the effective timeout: X-Timeout-Ms when present, else def,
// truncated to the route's hard cap (spec section 5.3).
func (c *Call) Timeout(def time.Duration) time.Duration {
	limit := SyncTimeoutMax
	if c.route.Transfer {
		limit = TransferTimeout
	}
	t := def
	if c.info.timeout > 0 {
		t = c.info.timeout
	}
	return min(t, limit)
}

// Context returns the request context bounded by Timeout(def) measured from
// the arrival of the request.
func (c *Call) Context(def time.Duration) context.Context {
	ctx, cancel := context.WithDeadline(c.Request.Context(), c.info.start.Add(c.Timeout(def)))
	c.cancels = append(c.cancels, cancel)
	return ctx
}

// Started is when the request arrived.
func (c *Call) Started() time.Time { return c.info.start }

func (c *Call) release() {
	for _, cancel := range c.cancels {
		cancel()
	}
}

// RunBlocking runs fn on its own goroutine and abandons it when ctx ends
// (spec section 7): the request answers with timeout while fn finishes in
// the background through its own natural exit path.
func RunBlocking[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		v, err := fn()
		done <- outcome{v, err}
	}()
	select {
	case o := <-done:
		return o.value, o.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
