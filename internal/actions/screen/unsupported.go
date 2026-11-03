package screen

import (
	"image"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

type unsupportedBackend struct{}

func (unsupportedBackend) Displays() ([]Display, error) {
	return nil, protocol.Unsupported("screen capture is not supported on this platform")
}

func (unsupportedBackend) Capture(protocol.Rect) (*image.RGBA, error) {
	return nil, protocol.Unsupported("screen capture is not supported on this platform")
}

func (unsupportedBackend) Cursor() (Cursor, error) {
	return Cursor{}, protocol.Unsupported("cursor is not supported on this platform")
}

func (unsupportedBackend) CursorImage() (*CursorImage, error) {
	return nil, protocol.Unsupported("cursor is not supported on this platform")
}
