//go:build windows

package window

import (
	"runtime"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return win32Backend{} }

// win32Backend enumerates with EnumWindows (Z order) and reads the visible
// frame with DwmGetWindowAttribute (spec section 10).
type win32Backend struct{}

// shellClasses are desktop shell windows close_all never touches.
var shellClasses = map[string]bool{
	"Progman": true, "WorkerW": true, "Shell_TrayWnd": true, "Shell_SecondaryTrayWnd": true,
	"Windows.UI.Core.CoreWindow": true, "NotifyIconOverflowWindow": true, "TopLevelWindowForOverflowXamlIsland": true,
	"Windows.UI.Composition.DesktopWindowContentBridge": true, "XamlExplorerHostIslandWindow": true,
}

func toRect(r win.RECT) *protocol.Rect {
	return &protocol.Rect{X: int(r.Left), Y: int(r.Top), W: int(r.Right - r.Left), H: int(r.Bottom - r.Top)}
}

func (win32Backend) List(visibleOnly bool) ([]Window, error) {
	handles, err := win.TopLevelWindows()
	if err != nil {
		return nil, err
	}
	fg := win.Foreground()
	out := make([]Window, 0, len(handles))
	for _, h := range handles {
		visible := win.IsVisible(h) && !win.IsCloaked(h)
		if visibleOnly && !visible {
			continue
		}
		_, pid := win.ThreadProcess(h)
		w := Window{
			ID:        FormatID(uint64(h)),
			Title:     win.Text(h),
			Class:     win.ClassName(h),
			PID:       int(pid),
			Active:    h == fg,
			Minimized: win.IsIconic(h),
			Maximized: win.IsZoomed(h),
			Visible:   visible,
			Tool:      win.IsToolWindow(h),
		}
		w.Shell = shellClasses[w.Class]
		if !w.Minimized {
			if r, ok := win.FrameRect(h); ok {
				w.Rect = toRect(r)
			}
			if r, ok := win.ClientRect(h); ok {
				w.ClientRect = toRect(r)
			}
		}
		out = append(out, w)
	}
	return out, nil
}

func handle(id string) (win.HWND, error) {
	v, err := ParseID(id)
	if err != nil || v == 0 {
		return 0, protocol.Errorf(protocol.CodeInvalidRequest, "invalid window id %q", id)
	}
	h := win.HWND(v)
	if !win.IsWindow(h) {
		return 0, protocol.Errorf(protocol.CodeNotFound, "window %s does not exist", id)
	}
	return h, nil
}

func isForeground(h win.HWND) bool {
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		if win.Foreground() == h {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Activate follows spec 6.4: restore if minimized; SetForegroundWindow;
// retry attached to the foreground thread's input queue; retry after an
// injected Alt tap (which lifts the foreground lock); else foreground_lock.
func (win32Backend) Activate(id string) error {
	h, err := handle(id)
	if err != nil {
		return err
	}
	if win.IsIconic(h) {
		win.Show(h, win.SWRestore)
	}
	if win.SetForeground(h) && isForeground(h) {
		return nil
	}
	// AttachThreadInput applies to the calling OS thread.
	runtime.LockOSThread()
	self := win.CurrentThreadID()
	fgThread, _ := win.ThreadProcess(win.Foreground())
	attached := fgThread != 0 && fgThread != self && win.AttachThreadInput(self, fgThread, true)
	win.BringToTop(h)
	ok := win.SetForeground(h) && isForeground(h)
	if attached {
		win.AttachThreadInput(self, fgThread, false)
	}
	runtime.UnlockOSThread()
	if ok {
		return nil
	}
	const vkMenu = 0x12
	_ = win.SendKey(vkMenu, 0x38, 0)
	_ = win.SendKey(vkMenu, 0x38, win.KeyUp)
	if win.SetForeground(h) && isForeground(h) {
		return nil
	}
	return protocol.Errorf(protocol.CodeIOFailed, "Windows refused to bring window %s to the foreground", id).WithReason(protocol.ReasonForegroundLock)
}

func (win32Backend) Close(id string) error {
	h, err := handle(id)
	if err != nil {
		return err
	}
	return win.PostClose(h)
}

func (win32Backend) SetState(id string, state State) error {
	h, err := handle(id)
	if err != nil {
		return err
	}
	switch state {
	case StateMaximize:
		win.Show(h, win.SWMaximize)
	case StateMinimize:
		win.Show(h, win.SWMinimize)
	case StateRestore:
		win.Show(h, win.SWRestore)
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "state must be maximize, minimize or restore")
	}
	return nil
}

// SetRect converts the requested visible frame to GetWindowRect coordinates
// by adding the invisible borders DWM reports, then calls SetWindowPos.
func (win32Backend) SetRect(id string, r protocol.Rect) error {
	h, err := handle(id)
	if err != nil {
		return err
	}
	target := win.RECT{Left: int32(r.X), Top: int32(r.Y), Right: int32(r.X + r.W), Bottom: int32(r.Y + r.H)}
	if outer, ok := win.WindowRect(h); ok {
		if frame, ok := win.FrameRect(h); ok {
			target.Left -= frame.Left - outer.Left
			target.Top -= frame.Top - outer.Top
			target.Right += outer.Right - frame.Right
			target.Bottom += outer.Bottom - frame.Bottom
		}
	}
	return win.MoveWindow(h, target)
}
