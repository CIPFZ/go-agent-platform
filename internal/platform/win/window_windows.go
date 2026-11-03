//go:build windows

package win

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procIsZoomed                 = user32.NewProc("IsZoomed")
	procIsWindow                 = user32.NewProc("IsWindow")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetClientRect            = user32.NewProc("GetClientRect")
	procClientToScreen           = user32.NewProc("ClientToScreen")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetCurrentThreadId       = kernel32.NewProc("GetCurrentThreadId")
)

// Show commands and messages.
const (
	SWMaximize = 3
	SWMinimize = 6
	SWRestore  = 9
	WMClose    = 0x0010

	gwlExStyle        = -20
	wsExToolWindow    = 0x00000080
	dwmaExtendedFrame = 9
	dwmaCloaked       = 14

	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swpNoOwnerZOrder = 0x0200
)

// HWND is a window handle.
type HWND uintptr

var (
	enumMu       sync.Mutex
	enumList     []HWND
	enumCallback = windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
		enumList = append(enumList, HWND(hwnd))
		return 1
	})
)

// TopLevelWindows returns all top-level windows in Z order, topmost first.
func TopLevelWindows() ([]HWND, error) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumList = nil
	if r, err := Call(procEnumWindows, enumCallback, 0); r == 0 {
		return nil, lastErr(err)
	}
	return append([]HWND(nil), enumList...), nil
}

func boolCall(p *windows.LazyProc, h HWND) bool {
	r, _ := Call(p, uintptr(h))
	return r != 0
}

// IsWindow, IsVisible, IsIconic and IsZoomed wrap the user32 predicates.
func IsWindow(h HWND) bool  { return boolCall(procIsWindow, h) }
func IsVisible(h HWND) bool { return boolCall(procIsWindowVisible, h) }
func IsIconic(h HWND) bool  { return boolCall(procIsIconic, h) }
func IsZoomed(h HWND) bool  { return boolCall(procIsZoomed, h) }

// IsCloaked reports DWM cloaking (hidden UWP frames, other virtual desktops).
func IsCloaked(h HWND) bool {
	var cloaked uint32
	hr, err := Call(procDwmGetWindowAttribute, uintptr(h), dwmaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked))
	return err == nil && hr == 0 && cloaked != 0
}

// IsToolWindow reports WS_EX_TOOLWINDOW.
func IsToolWindow(h HWND) bool {
	style, _ := Call(procGetWindowLongPtrW, uintptr(h), uintptr(int32ToUintptr(gwlExStyle)))
	return style&wsExToolWindow != 0
}

func int32ToUintptr(v int32) uintptr { return uintptr(int(v)) }

// Text returns the window title.
func Text(h HWND) string {
	n, _ := Call(procGetWindowTextLengthW, uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	r, _ := Call(procGetWindowTextW, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:r])
}

// ClassName returns the window class name.
func ClassName(h HWND) string {
	buf := make([]uint16, 257)
	r, _ := Call(procGetClassNameW, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:r])
}

// ThreadProcess returns the creating thread and process ids.
func ThreadProcess(h HWND) (thread, pid uint32) {
	t, _ := Call(procGetWindowThreadProcessId, uintptr(h), uintptr(unsafe.Pointer(&pid)))
	return uint32(t), pid
}

// FrameRect returns the visible window bounds: DWM extended frame bounds
// (which exclude the invisible resize borders) or GetWindowRect.
func FrameRect(h HWND) (RECT, bool) {
	var r RECT
	if hr, err := Call(procDwmGetWindowAttribute, uintptr(h), dwmaExtendedFrame, uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); err == nil && hr == 0 {
		return r, true
	}
	return WindowRect(h)
}

// WindowRect returns GetWindowRect.
func WindowRect(h HWND) (RECT, bool) {
	var r RECT
	ok, _ := Call(procGetWindowRect, uintptr(h), uintptr(unsafe.Pointer(&r)))
	return r, ok != 0
}

// ClientRect returns the client area in screen coordinates.
func ClientRect(h HWND) (RECT, bool) {
	var r RECT
	if ok, _ := Call(procGetClientRect, uintptr(h), uintptr(unsafe.Pointer(&r))); ok == 0 {
		return r, false
	}
	p := POINT{}
	if ok, _ := Call(procClientToScreen, uintptr(h), uintptr(unsafe.Pointer(&p))); ok == 0 {
		return r, false
	}
	return RECT{Left: p.X, Top: p.Y, Right: p.X + r.Right, Bottom: p.Y + r.Bottom}, true
}

// Foreground returns the foreground window.
func Foreground() HWND {
	r, _ := Call(procGetForegroundWindow)
	return HWND(r)
}

// SetForeground calls SetForegroundWindow and reports success.
func SetForeground(h HWND) bool {
	r, _ := Call(procSetForegroundWindow, uintptr(h))
	return r != 0
}

// BringToTop calls BringWindowToTop.
func BringToTop(h HWND) { _, _ = Call(procBringWindowToTop, uintptr(h)) }

// Show calls ShowWindow.
func Show(h HWND, cmd int) { _, _ = Call(procShowWindow, uintptr(h), uintptr(cmd)) }

// PostClose posts WM_CLOSE.
func PostClose(h HWND) error {
	if r, err := Call(procPostMessageW, uintptr(h), WMClose, 0, 0); r == 0 {
		return lastErr(err)
	}
	return nil
}

// AttachThreadInput attaches or detaches the input queues of two threads.
func AttachThreadInput(from, to uint32, attach bool) bool {
	a := uintptr(0)
	if attach {
		a = 1
	}
	r, _ := Call(procAttachThreadInput, uintptr(from), uintptr(to), a)
	return r != 0
}

// CurrentThreadID returns GetCurrentThreadId.
func CurrentThreadID() uint32 {
	r, _ := Call(procGetCurrentThreadId)
	return uint32(r)
}

// MoveWindow sets the window rectangle (GetWindowRect coordinates).
func MoveWindow(h HWND, r RECT) error {
	if ok, err := Call(procSetWindowPos, uintptr(h), 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate|swpNoOwnerZOrder); ok == 0 {
		return lastErr(err)
	}
	return nil
}
