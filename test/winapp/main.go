//go:build windows

// Command winapp is the self-drawn test window of deploy/windows-e2e.ps1. It
// declares Per-Monitor V2 DPI awareness, covers the requested rectangle (by
// default the primary monitor) with a borderless topmost window, and appends
// the physical screen coordinates of every WM_LBUTTONDOWN to a log file, so
// click positions can be compared with the requested ones at each display
// scale (spec 13.1 acceptance 1).
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	gdi32   = windows.NewLazySystemDLL("gdi32.dll")
	kernel  = windows.NewLazySystemDLL("kernel32.dll")
	pSetDPI = user32.NewProc("SetProcessDpiAwarenessContext")
	pReg    = user32.NewProc("RegisterClassExW")
	pCreate = user32.NewProc("CreateWindowExW")
	pDef    = user32.NewProc("DefWindowProcW")
	pGet    = user32.NewProc("GetMessageW")
	pTrans  = user32.NewProc("TranslateMessage")
	pDisp   = user32.NewProc("DispatchMessageW")
	pQuit   = user32.NewProc("PostQuitMessage")
	pC2S    = user32.NewProc("ClientToScreen")
	pMetric = user32.NewProc("GetSystemMetrics")
	pCursor = user32.NewProc("LoadCursorW")
	pStock  = gdi32.NewProc("GetStockObject")
	pModule = kernel.NewProc("GetModuleHandleW")
)

const (
	wsPopup        = 0x80000000
	wsVisible      = 0x10000000
	wsExTopmost    = 0x00000008
	wmDestroy      = 0x0002
	wmClose        = 0x0010
	wmLButtonDown  = 0x0201
	whiteBrush     = 0
	idcCross       = 32515
	smCXScreen     = 0
	smCYScreen     = 1
	dpiPerMonitor2 = ^uintptr(3)
)

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}

type point struct{ x, y int32 }

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
	_       uint32
}

var logFile *os.File

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmLButtonDown:
		p := point{x: int32(int16(lParam & 0xffff)), y: int32(int16((lParam >> 16) & 0xffff))}
		pC2S.Call(hwnd, uintptr(unsafe.Pointer(&p)))
		fmt.Fprintf(logFile, "%d,%d\n", p.x, p.y)
		logFile.Sync()
		return 0
	case wmClose:
		fmt.Fprintln(logFile, "close")
		logFile.Sync()
	case wmDestroy:
		pQuit.Call(0)
		return 0
	}
	r, _, _ := pDef.Call(hwnd, message, wParam, lParam)
	return r
}

func main() {
	logPath := flag.String("log", "winapp-clicks.log", "click log")
	title := flag.String("title", "vm-agent-dpi-test", "window title")
	x := flag.Int("x", 0, "left")
	y := flag.Int("y", 0, "top")
	w := flag.Int("w", 0, "width (default: primary monitor)")
	h := flag.Int("h", 0, "height (default: primary monitor)")
	flag.Parse()
	runtime.LockOSThread()
	pSetDPI.Call(dpiPerMonitor2)
	var err error
	logFile, err = os.OpenFile(*logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *w == 0 || *h == 0 {
		cx, _, _ := pMetric.Call(smCXScreen)
		cy, _, _ := pMetric.Call(smCYScreen)
		*w, *h = int(cx), int(cy)
	}
	instance, _, _ := pModule.Call(0)
	className, _ := syscall.UTF16PtrFromString("VmAgentWinApp")
	cursor, _, _ := pCursor.Call(0, idcCross)
	brush, _, _ := pStock.Call(whiteBrush)
	wc := wndClassEx{wndProc: syscall.NewCallback(wndProc), instance: instance, cursor: cursor, background: brush, className: className}
	wc.size = uint32(unsafe.Sizeof(wc))
	if r, _, err := pReg.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		fmt.Fprintln(os.Stderr, "RegisterClassEx:", err)
		os.Exit(1)
	}
	titlePtr, _ := syscall.UTF16PtrFromString(*title)
	hwnd, _, err := pCreate.Call(wsExTopmost, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(titlePtr)),
		wsPopup|wsVisible, uintptr(int32(*x)), uintptr(int32(*y)), uintptr(int32(*w)), uintptr(int32(*h)), 0, 0, instance, 0)
	if hwnd == 0 {
		fmt.Fprintln(os.Stderr, "CreateWindowEx:", err)
		os.Exit(1)
	}
	var m msg
	for {
		r, _, _ := pGet.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTrans.Call(uintptr(unsafe.Pointer(&m)))
		pDisp.Call(uintptr(unsafe.Pointer(&m)))
	}
}
