//go:build linux

package x11

import (
	"math"
	"strconv"
	"strings"

	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xinerama"
	"github.com/jezek/xgb/xproto"
)

// Monitor is one output in root-window coordinates.
type Monitor struct {
	X, Y, W, H int
	// MMWidth and MMHeight are the physical size, 0 when unknown.
	MMWidth, MMHeight int
}

// Monitors lists the outputs with the primary first. Xinerama (which the X
// server emulates from RandR, listing the RandR primary first) is the source
// of truth for geometry and order; this is also the origin convention of
// github.com/kbinani/screenshot, used for capture. RandR monitors supply the
// physical size. Without either extension the root window is one monitor.
func (c *Conn) Monitors() ([]Monitor, error) {
	var mons []Monitor
	if c.HasXinerama {
		active, err := xinerama.IsActive(c.X).Reply()
		if err != nil {
			return nil, c.Check(err)
		}
		if active.State != 0 {
			screens, err := xinerama.QueryScreens(c.X).Reply()
			if err != nil {
				return nil, c.Check(err)
			}
			for _, s := range screens.ScreenInfo {
				mons = append(mons, Monitor{X: int(s.XOrg), Y: int(s.YOrg), W: int(s.Width), H: int(s.Height)})
			}
		}
	}
	if len(mons) == 0 {
		mons = []Monitor{{W: int(c.Screen.WidthInPixels), H: int(c.Screen.HeightInPixels),
			MMWidth: int(c.Screen.WidthInMillimeters), MMHeight: int(c.Screen.HeightInMillimeters)}}
	}
	if c.HasRandR {
		if reply, err := randr.GetMonitors(c.X, c.Root, true).Reply(); err == nil {
			for i := range mons {
				for _, rm := range reply.Monitors {
					if int(rm.X) == mons[i].X && int(rm.Y) == mons[i].Y && int(rm.Width) == mons[i].W && int(rm.Height) == mons[i].H {
						mons[i].MMWidth, mons[i].MMHeight = int(rm.WidthInMillimeters), int(rm.HeightInMillimeters)
					}
				}
			}
		} else if err := c.Check(err); err != nil && !c.Alive() {
			return nil, err
		}
	}
	return mons, nil
}

// Origin returns the root-window position of the API origin: the top-left
// corner of the primary monitor.
func (c *Conn) Origin() (int, int, error) {
	mons, err := c.Monitors()
	if err != nil {
		return 0, 0, err
	}
	return mons[0].X, mons[0].Y, nil
}

// XftDPI returns the Xft.dpi resource from RESOURCE_MANAGER, or 0.
func (c *Conn) XftDPI() float64 {
	reply, err := c.Property(c.Root, "RESOURCE_MANAGER")
	if err != nil || reply == nil {
		return 0
	}
	for _, line := range strings.Split(string(reply.Value), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "Xft.dpi" {
			if dpi, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && dpi > 0 {
				return dpi
			}
		}
	}
	return 0
}

// Scale returns the DPI scale of m: Xft.dpi/96 when the resource is set,
// otherwise an estimate from the RandR physical size rounded to a quarter
// step, otherwise 1 (spec section 10).
func (c *Conn) Scale(m Monitor, xftDPI float64) float64 {
	if xftDPI > 0 {
		return roundQuarter(xftDPI / 96)
	}
	if m.MMWidth > 0 && m.W > 0 {
		return max(roundQuarter(float64(m.W)*25.4/float64(m.MMWidth)/96), 1)
	}
	return 1
}

func roundQuarter(v float64) float64 { return math.Round(v*4) / 4 }

// Pointer returns the pointer position in root coordinates.
func (c *Conn) Pointer() (int, int, error) {
	reply, err := xproto.QueryPointer(c.X, c.Root).Reply()
	if err != nil {
		return 0, 0, c.Check(err)
	}
	return int(reply.RootX), int(reply.RootY), nil
}
