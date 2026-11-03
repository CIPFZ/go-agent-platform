//go:build windows

package screen

import (
	"image"

	"github.com/kbinani/screenshot"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return gdiBackend{} }

// gdiBackend captures with github.com/kbinani/screenshot (GDI BitBlt). The
// process is Per-Monitor V2 DPI aware (spec 4.1), so every coordinate here
// is in physical pixels and the primary monitor's top-left is (0, 0).
type gdiBackend struct{}

func (gdiBackend) Displays() ([]Display, error) {
	mons, err := win.Monitors()
	if err != nil {
		return nil, err
	}
	out := make([]Display, len(mons))
	for i, m := range mons {
		out[i] = Display{
			Index:   i,
			Bounds:  protocol.Rect{X: int(m.Rect.Left), Y: int(m.Rect.Top), W: int(m.Rect.Right - m.Rect.Left), H: int(m.Rect.Bottom - m.Rect.Top)},
			Scale:   m.Scale,
			Primary: m.Primary,
		}
	}
	return out, nil
}

func (gdiBackend) Capture(r protocol.Rect) (*image.RGBA, error) {
	return screenshot.Capture(r.X, r.Y, r.W, r.H)
}

func (gdiBackend) Cursor() (Cursor, error) {
	x, y, visible, _, err := win.CursorState()
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{X: x, Y: y, Visible: visible}, nil
}

func (gdiBackend) CursorImage() (*CursorImage, error) {
	_, _, _, handle, err := win.CursorState()
	if err != nil {
		return nil, err
	}
	img, hot, err := win.CursorImage(handle)
	if err != nil {
		return nil, err
	}
	return &CursorImage{Image: img, Hotspot: hot}, nil
}
