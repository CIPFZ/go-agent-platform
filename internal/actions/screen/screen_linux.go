//go:build linux

package screen

import (
	"errors"
	"image"

	"github.com/jezek/xgb/xfixes"
	"github.com/kbinani/screenshot"

	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return x11Backend{} }

// x11Backend captures through github.com/kbinani/screenshot (xgb GetImage,
// MIT-SHM when available), whose coordinates are relative to the first
// Xinerama screen, the same origin x11.Conn.Monitors uses.
type x11Backend struct{}

func (x11Backend) Displays() ([]Display, error) {
	c, err := x11.Shared()
	if err != nil {
		return nil, noDisplay(err)
	}
	mons, err := c.Monitors()
	if err != nil {
		return nil, err
	}
	ox, oy := mons[0].X, mons[0].Y
	dpi := c.XftDPI()
	out := make([]Display, len(mons))
	for i, m := range mons {
		out[i] = Display{
			Index:   i,
			Bounds:  protocol.Rect{X: m.X - ox, Y: m.Y - oy, W: m.W, H: m.H},
			Scale:   c.Scale(m, dpi),
			Primary: i == 0,
		}
	}
	return out, nil
}

func (x11Backend) Capture(r protocol.Rect) (*image.RGBA, error) {
	img, err := screenshot.Capture(r.X, r.Y, r.W, r.H)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func (x11Backend) Cursor() (Cursor, error) {
	c, err := x11.Shared()
	if err != nil {
		return Cursor{}, noDisplay(err)
	}
	x, y, err := c.Pointer()
	if err != nil {
		return Cursor{}, err
	}
	ox, oy, err := c.Origin()
	if err != nil {
		return Cursor{}, err
	}
	visible := true
	if c.HasXFixes {
		if img, err := xfixes.GetCursorImage(c.X).Reply(); err == nil {
			visible = false
			for _, px := range img.CursorImage {
				if px>>24 != 0 {
					visible = true
					break
				}
			}
		}
	}
	return Cursor{X: x - ox, Y: y - oy, Visible: visible}, nil
}

// CursorImage converts the XFixes cursor (premultiplied ARGB words) to RGBA,
// which image/draw also treats as premultiplied.
func (x11Backend) CursorImage() (*CursorImage, error) {
	c, err := x11.Shared()
	if err != nil {
		return nil, noDisplay(err)
	}
	if !c.HasXFixes {
		return nil, errors.New("XFixes extension is not available")
	}
	reply, err := xfixes.GetCursorImage(c.X).Reply()
	if err != nil {
		return nil, c.Check(err)
	}
	w, h := int(reply.Width), int(reply.Height)
	if w == 0 || h == 0 || len(reply.CursorImage) < w*h {
		return nil, errors.New("empty cursor image")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, px := range reply.CursorImage[:w*h] {
		img.Pix[i*4+0] = uint8(px >> 16)
		img.Pix[i*4+1] = uint8(px >> 8)
		img.Pix[i*4+2] = uint8(px)
		img.Pix[i*4+3] = uint8(px >> 24)
	}
	return &CursorImage{Image: img, Hotspot: image.Pt(int(reply.Xhot), int(reply.Yhot))}, nil
}

func noDisplay(err error) error {
	return protocol.Errorf(protocol.CodeNoDisplay, "%v", err)
}
