//go:build !linux && !windows

package clipboard

import (
	"context"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return unsupportedBackend{} }

type unsupportedBackend struct{}

func errUnsupported() error {
	return protocol.Unsupported("clipboard is not supported on this platform")
}

func (unsupportedBackend) Read(context.Context) (Content, error)   { return Content{}, errUnsupported() }
func (unsupportedBackend) WriteText(context.Context, string) error { return errUnsupported() }
func (unsupportedBackend) WritePNG(context.Context, []byte) error  { return errUnsupported() }
