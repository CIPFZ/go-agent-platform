//go:build linux

package screen

import (
	"context"
	"image"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/platform/wayland"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// portalTimeout bounds one portal screenshot; the caller's request timeout
// applies on top, as backend calls run on abandonable goroutines.
const portalTimeout = 15 * time.Second

// waylandBackend captures through the xdg-desktop-portal Screenshot
// interface (spec 13.3). The desktop is one display the size of the
// screenshot; the pointer position is not available to Wayland clients, so
// cursors are neither reported nor drawn (as agreed for phase 3).
type waylandBackend struct{}

func (waylandBackend) Displays() ([]Display, error) {
	ctx, cancel := context.WithTimeout(context.Background(), portalTimeout)
	defer cancel()
	w, h, err := wayland.ScreenSize(ctx)
	if err != nil {
		return nil, err
	}
	return []Display{{Index: 0, Bounds: protocol.Rect{W: w, H: h}, Scale: 1, Primary: true}}, nil
}

func (waylandBackend) Capture(r protocol.Rect) (*image.RGBA, error) {
	ctx, cancel := context.WithTimeout(context.Background(), portalTimeout)
	defer cancel()
	img, err := wayland.Screenshot(ctx)
	if err != nil {
		return nil, err
	}
	return wayland.ToRGBA(img, image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H).Intersect(img.Bounds().Sub(img.Bounds().Min))), nil
}

func (waylandBackend) Cursor() (Cursor, error) {
	return Cursor{}, protocol.Unsupported("the pointer position is not available in a Wayland session")
}

func (waylandBackend) CursorImage() (*CursorImage, error) {
	return nil, protocol.Unsupported("the cursor image is not available in a Wayland session")
}
