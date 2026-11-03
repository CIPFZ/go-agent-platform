//go:build windows

package process

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrNoHandler means no application is associated with the target.
var ErrNoHandler = errors.New("no application is associated with this file type or URL scheme")

// OpenResult reports what Open started.
type OpenResult struct {
	PID     int    `json:"pid,omitempty"`
	Handler string `json:"handler,omitempty"`
}

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteExW")
)

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskFlagNoUI       = 0x00000400
	swShowNormal          = 1
	errorNoAssociation    = windows.Errno(1155)
)

// Open opens a path or URL with ShellExecuteEx "open" (spec section 6.5).
// The pid is reported when the shell starts a new process for it.
func Open(target string, _ bool, _ []string) (OpenResult, error) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return OpenResult{}, err
	}
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskFlagNoUI, lpVerb: verb, lpFile: file, nShow: swShowNormal}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if err := procShellExecuteW.Find(); err != nil {
		return OpenResult{}, err
	}
	if r, _, callErr := procShellExecuteW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(callErr, errorNoAssociation) {
			return OpenResult{}, ErrNoHandler
		}
		return OpenResult{}, callErr
	}
	res := OpenResult{Handler: "shell"}
	if info.hProcess != 0 {
		if pid, err := windows.GetProcessId(info.hProcess); err == nil {
			res.PID = int(pid)
		}
		_ = windows.CloseHandle(info.hProcess)
	}
	return res, nil
}
