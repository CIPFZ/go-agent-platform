//go:build !linux && !windows

package process

import "errors"

// ErrNoHandler means no application is associated with the target.
var ErrNoHandler = errors.New("no application is associated with this file type or URL scheme")

// OpenResult reports what Open started.
type OpenResult struct {
	PID     int    `json:"pid,omitempty"`
	Handler string `json:"handler,omitempty"`
}

// Open is not supported on this platform.
func Open(string, bool, []string) (OpenResult, error) { return OpenResult{}, ErrUnsupported }
