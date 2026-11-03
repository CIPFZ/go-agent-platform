//go:build !linux && !windows

package window

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

func newBackend() Backend { return unsupportedBackend{} }

type unsupportedBackend struct{}

func errUnsupported() error {
	return protocol.Unsupported("window management is not supported on this platform")
}

func (unsupportedBackend) List(bool) ([]Window, error)         { return nil, errUnsupported() }
func (unsupportedBackend) Activate(string) error               { return errUnsupported() }
func (unsupportedBackend) Close(string) error                  { return errUnsupported() }
func (unsupportedBackend) SetState(string, State) error        { return errUnsupported() }
func (unsupportedBackend) SetRect(string, protocol.Rect) error { return errUnsupported() }
