//go:build windows

package win

import (
	"strings"
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

var procSetThreadDesktop = user32.NewProc("SetThreadDesktop")

// attachedDesktop is the desktop handle the input thread holds.
var attachedDesktop uintptr

// AttachInputDesktop makes the calling thread belong to the desktop that is
// receiving input, which SendInput requires. It is for one dedicated OS
// thread (runtime.LockOSThread) of a process allowed to open that desktop:
// on the lock screen that means SYSTEM.
func AttachInputDesktop() error {
	const genericAll = 0x10000000
	desk, err := Call(procOpenInputDesktop, 0, 0, genericAll)
	if desk == 0 {
		return lastErr(err)
	}
	if r, err := Call(procSetThreadDesktop, desk); r == 0 {
		_, _ = Call(procCloseDesktop, desk)
		return lastErr(err)
	}
	if attachedDesktop != 0 {
		_, _ = Call(procCloseDesktop, attachedDesktop)
	}
	attachedDesktop = desk
	return nil
}

// SessionLocked reports whether the session shows the lock screen, from the
// session manager's own record (WTSSessionInfoEx). known is false when the
// state cannot be read.
func SessionLocked(session uint32) (locked, known bool) {
	const (
		wtsSessionInfoEx = 25
		// SessionFlags follows Level (4 bytes, padded to 8), SessionId and
		// SessionState in WTSINFOEXW.
		flagsOffset = 16
		stateLock   = 0
		stateUnlock = 1
	)
	var buf *byte
	var size uint32
	if r, _ := Call(procWTSQuerySessionInformationW, wtsCurrentServerHandle, uintptr(session), wtsSessionInfoEx,
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&size))); r == 0 || buf == nil {
		return false, false
	}
	defer Call(procWTSFreeMemory, uintptr(unsafe.Pointer(buf)))
	if size < flagsOffset+4 {
		return false, false
	}
	switch *(*uint32)(unsafe.Add(unsafe.Pointer(buf), flagsOffset)) {
	case stateLock:
		return true, true
	case stateUnlock:
		return false, true
	}
	return false, false
}

// ProcessInSession reports whether a process with the given executable name
// runs in the session.
func ProcessInSession(name string, session uint32) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), name) {
			continue
		}
		var s uint32
		if windows.ProcessIdToSessionId(e.ProcessID, &s) == nil && s == session {
			return true
		}
	}
	return false
}
