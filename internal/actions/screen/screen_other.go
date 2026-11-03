//go:build !linux && !windows

package screen

import (
	"image"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return unsupportedBackend{} }

type unsupportedBackend struct{}

func errUnsupported() error {
	return protocol.Unsupported("screen capture is not supported on this platform")
}

func (unsupportedBackend) Displays() ([]Display, error)               { return nil, errUnsupported() }
func (unsupportedBackend) Capture(protocol.Rect) (*image.RGBA, error) { return nil, errUnsupported() }
func (unsupportedBackend) Cursor() (Cursor, error)                    { return Cursor{}, errUnsupported() }
func (unsupportedBackend) CursorImage() (*CursorImage, error)         { return nil, errUnsupported() }
