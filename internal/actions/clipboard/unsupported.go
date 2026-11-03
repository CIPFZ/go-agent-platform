package clipboard

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

type unsupportedBackend struct{}

func errUnsupported() error {
	return protocol.Unsupported("clipboard is not supported on this platform")
}

func (unsupportedBackend) Read() (Content, error) { return Content{}, errUnsupported() }
func (unsupportedBackend) WriteText(string) error { return errUnsupported() }
func (unsupportedBackend) WritePNG([]byte) error  { return errUnsupported() }
