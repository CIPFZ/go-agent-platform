//go:build windows

package remote

import (
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// PlatformVirtualDisplay controls the driver installed by
// deploy/windows/virtual-display.ps1.
func PlatformVirtualDisplay() VirtualDisplay { return amyuniDisplay{} }

type amyuniDisplay struct{}

func (amyuniDisplay) Installed() bool { return win.VirtualDisplayInstalled() }
func (amyuniDisplay) Active() bool    { return win.DisplayActive() }
func (amyuniDisplay) Plugged() int    { return len(win.VirtualDisplays()) }
func (amyuniDisplay) Unplug() error   { return win.UnplugVirtualDisplay() }

func (amyuniDisplay) RealDisplay() bool {
	mons, err := win.Monitors()
	return err == nil && len(mons) > len(win.VirtualDisplays())
}

func (amyuniDisplay) Plug(size protocol.Rect, scale float64) error {
	name, err := win.PlugVirtualDisplay(5 * time.Second)
	if err != nil {
		return err
	}
	// Windows remembers the mode and scale of a monitor it has seen; they are
	// set anyway for the first time and in case they were changed.
	if size.W > 0 && size.H > 0 {
		if err := win.SetDisplayMode(name, size.W, size.H); err != nil {
			logf("virtual display: %v", err)
		}
	}
	if scale > 0 {
		if err := win.SetDisplayScale(name, scale); err != nil {
			logf("virtual display: %v", err)
		}
	}
	return nil
}
