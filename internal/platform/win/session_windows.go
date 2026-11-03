//go:build windows

package win

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	desktopReadObjects = 0x0001
	uoiName            = 2
)

// InputDesktopIsDefault reports whether the desktop receiving input is the
// user's "Default" desktop. OpenInputDesktop fails, or returns "Winlogon" or
// "Screen-saver", while the session is locked or a secure desktop (UAC
// prompt, Ctrl+Alt+Del) is shown (spec section 10).
func InputDesktopIsDefault() bool {
	desk, _ := Call(procOpenInputDesktop, 0, 0, desktopReadObjects)
	if desk == 0 {
		return false
	}
	defer Call(procCloseDesktop, desk)
	var name [64]uint16
	var needed uint32
	if r, _ := Call(procGetUserObjectInformationW, desk, uoiName, uintptr(unsafe.Pointer(&name[0])), unsafe.Sizeof(name), uintptr(unsafe.Pointer(&needed))); r == 0 {
		return false
	}
	return windows.UTF16ToString(name[:]) == "Default"
}

const (
	wtsCurrentServerHandle = 0
	wtsCurrentSession      = 0xFFFFFFFF
	wtsClientProtocolType  = 16
)

// ClientProtocolType returns WTSClientProtocolType of the current session:
// 0 console, 1 ICA, 2 RDP.
func ClientProtocolType() (uint16, error) {
	var buf *uint16
	var size uint32
	if r, err := Call(procWTSQuerySessionInformationW, wtsCurrentServerHandle, wtsCurrentSession, wtsClientProtocolType, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&size))); r == 0 {
		return 0, lastErr(err)
	}
	defer Call(procWTSFreeMemory, uintptr(unsafe.Pointer(buf)))
	if size < 2 {
		return 0, nil
	}
	return *buf, nil
}
