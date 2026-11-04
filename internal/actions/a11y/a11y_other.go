//go:build !linux && !windows

package a11y

import (
	"os/exec"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

var platform collector = unsupported{}

type unsupported struct{}

func (unsupported) command(Request) (*exec.Cmd, error) {
	return nil, protocol.Unsupported("accessibility trees are not supported on this platform")
}

// Available reports whether accessibility trees can be collected.
func Available() bool { return false }

func walk(Request) (*Result, error) {
	return nil, protocol.Unsupported("accessibility trees are not supported on this platform")
}
