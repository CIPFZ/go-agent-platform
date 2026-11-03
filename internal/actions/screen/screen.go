// Package screen implements /v1/screen: display enumeration, capture and the
// cursor. Coordinates are physical pixels of the virtual desktop with the
// primary display's top-left corner as origin (spec D1, section 6.2).
package screen

import (
	"image"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Display is one monitor. Index 0 is the primary display.
type Display struct {
	Index   int           `json:"index"`
	Bounds  protocol.Rect `json:"bounds"`
	Scale   float64       `json:"scale"`
	Primary bool          `json:"primary"`
}

// Cursor is the pointer position and visibility.
type Cursor struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Visible bool `json:"visible"`
}

// CursorImage is the current pointer shape for overlaying on captures.
type CursorImage struct {
	Image   *image.RGBA
	Hotspot image.Point
}

// Backend is the platform capture implementation. Calls may block; callers
// run them under a timeout on their own goroutine.
type Backend interface {
	Displays() ([]Display, error)
	Capture(r protocol.Rect) (*image.RGBA, error)
	Cursor() (Cursor, error)
	CursorImage() (*CursorImage, error)
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }
