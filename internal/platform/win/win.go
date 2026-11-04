//go:build windows

// Package win holds the Win32 declarations shared by the Windows backends of
// the screen, input, window, clipboard and session packages. Everything is
// called through lazily loaded system DLLs, so vm-agent has no runtime
// dependency beyond Windows itself (spec D3).
package win

import (
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	wtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetThreadDpiAwarenessContext  = user32.NewProc("GetThreadDpiAwarenessContext")
	procAreDpiAwarenessContextsEqual  = user32.NewProc("AreDpiAwarenessContextsEqual")
	procIsProcessDPIAware             = user32.NewProc("IsProcessDPIAware")
	procEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procGetDpiForMonitor              = shcore.NewProc("GetDpiForMonitor")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procGetCursorInfo                 = user32.NewProc("GetCursorInfo")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procSetCursorPos                  = user32.NewProc("SetCursorPos")
	procGetIconInfo                   = user32.NewProc("GetIconInfo")
	procDrawIconEx                    = user32.NewProc("DrawIconEx")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procFillRect                      = user32.NewProc("FillRect")
	procSendInput                     = user32.NewProc("SendInput")
	procMapVirtualKeyW                = user32.NewProc("MapVirtualKeyW")
	procVkKeyScanW                    = user32.NewProc("VkKeyScanW")
	procOpenInputDesktop              = user32.NewProc("OpenInputDesktop")
	procCloseDesktop                  = user32.NewProc("CloseDesktop")
	procGetUserObjectInformationW     = user32.NewProc("GetUserObjectInformationW")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procGetObjectW         = gdi32.NewProc("GetObjectW")
	procGetStockObject     = gdi32.NewProc("GetStockObject")

	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")

	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory               = wtsapi32.NewProc("WTSFreeMemory")
)

// RECT is the Win32 RECT.
type RECT struct{ Left, Top, Right, Bottom int32 }

// POINT is the Win32 POINT.
type POINT struct{ X, Y int32 }

// Call invokes a lazily loaded procedure, returning ERROR_PROC_NOT_FOUND
// when the running Windows version lacks it. Otherwise the error is the
// thread's last error and is never nil, even on success: judge success by
// the return value (see HResult for COM-style functions). Like LazyProc.Call, pointer
// arguments must be converted with uintptr(unsafe.Pointer(p)) inside the call
// expression; uintptrescapes keeps them alive for the duration of the call.
//
//go:uintptrescapes
func Call(p *windows.LazyProc, args ...uintptr) (uintptr, error) {
	if err := p.Find(); err != nil {
		return 0, err
	}
	r, _, err := p.Call(args...)
	return r, err
}

// HResult calls a function returning an HRESULT and reports whether it
// returned S_OK; a function missing from this Windows version fails.
//
//go:uintptrescapes
func HResult(p *windows.LazyProc, args ...uintptr) bool {
	if p.Find() != nil {
		return false
	}
	hr, _, _ := p.Call(args...)
	return hr == 0
}

// lastErr returns e unless it is the zero errno that Call reports on success.
func lastErr(e error) error {
	if errno, ok := e.(syscall.Errno); ok && errno == 0 {
		return syscall.EINVAL
	}
	return e
}
