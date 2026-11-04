//go:build windows

package win

import (
	"errors"
	"sort"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPI awareness contexts (windef.h).
const (
	dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3) // (DPI_AWARENESS_CONTEXT)-4
	dpiAwarenessContextPerMonitorAware   = ^uintptr(2) // -3
	dpiAwarenessContextSystemAware       = ^uintptr(1) // -2
	dpiAwarenessContextUnaware           = ^uintptr(0) // -1
)

var (
	dpiOnce      sync.Once
	dpiAwareness = "unaware"
)

// InitDPIAwareness declares Per-Monitor V2 DPI awareness, falling back to
// SetProcessDPIAware (spec 4.1), and returns the level in effect:
// "per_monitor_v2", "per_monitor", "system" or "unaware". Only the first
// call has an effect.
func InitDPIAwareness() string {
	dpiOnce.Do(func() {
		if r, _ := Call(procSetProcessDpiAwarenessContext, dpiAwarenessContextPerMonitorAwareV2); r == 0 {
			_, _ = Call(procSetProcessDPIAware)
		}
		dpiAwareness = currentDPIAwareness()
	})
	return dpiAwareness
}

// DPIAwareness returns the level recorded by InitDPIAwareness.
func DPIAwareness() string { return InitDPIAwareness() }

func currentDPIAwareness() string {
	if ctx, _ := Call(procGetThreadDpiAwarenessContext); ctx != 0 {
		for _, c := range []struct {
			ctx  uintptr
			name string
		}{
			{dpiAwarenessContextPerMonitorAwareV2, "per_monitor_v2"},
			{dpiAwarenessContextPerMonitorAware, "per_monitor"},
			{dpiAwarenessContextSystemAware, "system"},
			{dpiAwarenessContextUnaware, "unaware"},
		} {
			if eq, _ := Call(procAreDpiAwarenessContextsEqual, ctx, c.ctx); eq != 0 {
				return c.name
			}
		}
	}
	if aware, _ := Call(procIsProcessDPIAware); aware != 0 {
		return "system"
	}
	return "unaware"
}

// Monitor is one display in physical virtual-desktop coordinates.
type Monitor struct {
	Rect    RECT
	Scale   float64
	Primary bool
}

type monitorInfoEx struct {
	cbSize    uint32
	rcMonitor RECT
	rcWork    RECT
	dwFlags   uint32
	szDevice  [32]uint16
}

const monitorInfoPrimary = 1

var (
	monitorMu       sync.Mutex
	monitorList     []Monitor
	monitorCallback = windows.NewCallback(func(hmon, hdc, rect, lparam uintptr) uintptr {
		info := monitorInfoEx{}
		info.cbSize = uint32(unsafe.Sizeof(info))
		if r, _ := Call(procGetMonitorInfoW, hmon, uintptr(unsafe.Pointer(&info))); r == 0 {
			return 1
		}
		scale := 1.0
		var dpiX, dpiY uint32
		if HResult(procGetDpiForMonitor, hmon, 0 /* MDT_EFFECTIVE_DPI */, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY))) && dpiX > 0 {
			scale = float64(dpiX) / 96
		}
		monitorList = append(monitorList, Monitor{Rect: info.rcMonitor, Scale: scale, Primary: info.dwFlags&monitorInfoPrimary != 0})
		return 1
	})
)

// Monitors enumerates displays with the primary first and the rest ordered
// left to right, top to bottom.
func Monitors() ([]Monitor, error) {
	monitorMu.Lock()
	defer monitorMu.Unlock()
	monitorList = nil
	if r, err := Call(procEnumDisplayMonitors, 0, 0, monitorCallback, 0); r == 0 {
		return nil, lastErr(err)
	}
	out := append([]Monitor(nil), monitorList...)
	if len(out) == 0 {
		return nil, errors.New("no display monitors")
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Primary != out[j].Primary {
			return out[i].Primary
		}
		if out[i].Rect.Left != out[j].Rect.Left {
			return out[i].Rect.Left < out[j].Rect.Left
		}
		return out[i].Rect.Top < out[j].Rect.Top
	})
	return out, nil
}

// System metrics for the virtual screen.
const (
	SMXVirtualScreen  = 76
	SMYVirtualScreen  = 77
	SMCXVirtualScreen = 78
	SMCYVirtualScreen = 79
)

// VirtualScreen returns the bounding rectangle of all monitors.
func VirtualScreen() RECT {
	x, _ := Call(procGetSystemMetrics, SMXVirtualScreen)
	y, _ := Call(procGetSystemMetrics, SMYVirtualScreen)
	w, _ := Call(procGetSystemMetrics, SMCXVirtualScreen)
	h, _ := Call(procGetSystemMetrics, SMCYVirtualScreen)
	return RECT{Left: int32(x), Top: int32(y), Right: int32(x) + int32(w), Bottom: int32(y) + int32(h)}
}
