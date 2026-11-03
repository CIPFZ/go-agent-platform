// Package protocol defines the vm-agent wire format (spec section 5): the
// response envelope, error codes, and the request and result bodies of the
// /v1 endpoints. It has no task, lease or idempotency types; every call is a
// synchronous request and response.
package protocol

import "regexp"

// APIVersion is the path prefix version, independent of the service version.
const APIVersion = "v1"

// Header names of spec sections 5.1, 5.3, 5.4 and 5.6.
const (
	HeaderRequestID    = "X-Request-Id"
	HeaderTimeoutMS    = "X-Timeout-Ms"
	HeaderAgentVersion = "X-Agent-Version"
	HeaderDedupHit     = "X-Dedup-Hit"

	HeaderResultWidth  = "X-Result-Width"
	HeaderResultHeight = "X-Result-Height"
	HeaderResultSHA256 = "X-Result-Sha256"
	HeaderResultSize   = "X-Result-Size"
	HeaderCursorX      = "X-Cursor-X"
	HeaderCursorY      = "X-Cursor-Y"
	HeaderCapturedAt   = "X-Captured-At"
	HeaderFileMtime    = "X-File-Mtime"
)

// RequestIDPattern is the accepted X-Request-Id format (spec section 5.3).
var RequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{16,128}$`)

// Response is the JSON envelope of every non-streaming response. Successful
// responses carry Result and ElapsedMS; failed ones carry Error.
type Response struct {
	OK        bool   `json:"ok"`
	RequestID string `json:"request_id"`
	ElapsedMS *int64 `json:"elapsed_ms,omitempty"`
	Result    any    `json:"result,omitempty"`
	Error     *Error `json:"error,omitempty"`
}

// Empty is the result of actions that return no data; it encodes as {}.
type Empty struct{}
