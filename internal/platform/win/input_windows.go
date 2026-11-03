//go:build windows

package win

import (
	"unsafe"
)

// INPUT structures of winuser.h for amd64: a 4-byte type, 4 bytes of
// padding, then a 32-byte union. Both variants below are padded to 40 bytes.
type mouseInput struct {
	typ       uint32
	_         uint32
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extra     uintptr
}

type keyboardInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	extra uintptr
	_     [8]byte
}

const (
	inputMouse    = 0
	inputKeyboard = 1

	MouseMove        = 0x0001
	MouseLeftDown    = 0x0002
	MouseLeftUp      = 0x0004
	MouseRightDown   = 0x0008
	MouseRightUp     = 0x0010
	MouseMiddleDown  = 0x0020
	MouseMiddleUp    = 0x0040
	MouseWheel       = 0x0800
	MouseHWheel      = 0x1000
	MouseVirtualDesk = 0x4000
	MouseAbsolute    = 0x8000

	KeyExtended = 0x0001
	KeyUp       = 0x0002
	KeyUnicode  = 0x0004
	KeyScancode = 0x0008

	WheelDelta = 120
)

// SendMouse injects one mouse event.
func SendMouse(dx, dy int32, data uint32, flags uint32) error {
	in := mouseInput{typ: inputMouse, dx: dx, dy: dy, mouseData: data, flags: flags}
	return sendInput(uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

// SendKey injects one keyboard event.
func SendKey(vk, scan uint16, flags uint32) error {
	in := keyboardInput{typ: inputKeyboard, vk: vk, scan: scan, flags: flags}
	return sendInput(uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

// ErrInputBlocked means SendInput inserted no event, typically because the
// target is a higher-integrity window (UIPI) or the desktop is locked.
type ErrInputBlocked struct{ Err error }

func (e ErrInputBlocked) Error() string { return "SendInput inserted no event: " + e.Err.Error() }

//go:uintptrescapes
func sendInput(ptr uintptr, size uintptr) error {
	n, err := Call(procSendInput, 1, ptr, size)
	if n != 1 {
		return ErrInputBlocked{Err: lastErr(err)}
	}
	return nil
}

// VkKeyScan returns the virtual key and shift state (bit 0 shift, 1 ctrl,
// 2 alt) that produce r in the current layout; ok is false if none does.
func VkKeyScan(r rune) (vk uint8, shiftState uint8, ok bool) {
	v, _ := Call(procVkKeyScanW, uintptr(uint16(r)))
	if int16(v) == -1 {
		return 0, 0, false
	}
	return uint8(v), uint8(v >> 8), true
}

// ScanCode maps a virtual key to its scan code (MAPVK_VK_TO_VSC).
func ScanCode(vk uint16) uint16 {
	v, _ := Call(procMapVirtualKeyW, uintptr(vk), 0)
	return uint16(v)
}
