package protocol

import (
	"fmt"
	"net/http"
)

// Code is one of the error codes in spec section 5.5. No other value may be
// sent on the wire; every details.reason is listed in docs/errors.md.
type Code string

const (
	CodeInvalidRequest Code = "invalid_request"
	CodeNotFound       Code = "not_found"
	CodeAmbiguous      Code = "ambiguous"
	CodeConflict       Code = "conflict"
	CodeTooLarge       Code = "too_large"
	CodeUnsupported    Code = "unsupported"
	CodeNoDisplay      Code = "no_display"
	CodeBusy           Code = "busy"
	CodeTimeout        Code = "timeout"
	CodeIOFailed       Code = "io_failed"
	CodeInternal       Code = "internal"
)

// HTTPStatus returns the status code mandated for c by spec section 5.5.
func (c Code) HTTPStatus() int {
	switch c {
	case CodeInvalidRequest:
		return http.StatusBadRequest
	case CodeNotFound:
		return http.StatusNotFound
	case CodeAmbiguous, CodeConflict:
		return http.StatusConflict
	case CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeUnsupported:
		return http.StatusNotImplemented
	case CodeNoDisplay, CodeBusy:
		return http.StatusServiceUnavailable
	case CodeTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// Retryable reports the fixed retryable flag of c. timeout is decided per
// action by the caller of NewTimeout, so it reports false here.
func (c Code) Retryable() bool { return c == CodeNoDisplay || c == CodeBusy }

// Reasons used in details.reason. Keep in sync with docs/errors.md; the
// errors_doc_test.go test enforces it.
const (
	ReasonPolicyDenied      = "policy_denied"
	ReasonInFlight          = "in_flight"
	ReasonDedupStream       = "dedup_stream"
	ReasonDiskFull          = "disk_full"
	ReasonForegroundLock    = "foreground_lock"
	ReasonInvalidRequestID  = "invalid_request_id"
	ReasonInvalidTimeout    = "invalid_timeout"
	ReasonInvalidJSON       = "invalid_json"
	ReasonUnknownRoute      = "unknown_route"
	ReasonMethodNotAllowed  = "method_not_allowed"
	ReasonURLScheme         = "url_scheme"
	ReasonPathNotAbsolute   = "path_not_absolute"
	ReasonOutsideRoot       = "outside_root"
	ReasonRegionOutOfBounds = "region_out_of_bounds"
	ReasonUnknownKey        = "unknown_key"
	ReasonPermissionDenied  = "permission_denied"
	// ReasonClientDisconnected marks a request whose caller went away before
	// it finished; it is what a deduplicated retry replays.
	ReasonClientDisconnected = "client_disconnected"
)

// Error is the error object of an error response (spec section 5.4).
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf returns an Error with the code's default retryable flag.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Retryable: code.Retryable()}
}

// WithReason sets details.reason.
func (e *Error) WithReason(reason string) *Error { return e.WithDetail("reason", reason) }

// WithDetail sets one details entry.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

// Unsupported is shorthand for an unsupported error.
func Unsupported(format string, args ...any) *Error {
	return Errorf(CodeUnsupported, format, args...)
}
