//go:build windows

package input

import (
	"errors"
	"time"
	"unicode/utf16"

	"github.com/CIPFZ/go-agent-platform/internal/platform/win"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return sendInputBackend{} }

// sendInputBackend wraps user32.SendInput (spec section 10). Mouse moves use
// MOUSEEVENTF_ABSOLUTE|MOUSEEVENTF_VIRTUALDESK normalised over the virtual
// screen so secondary monitors at negative coordinates are reachable; text
// uses KEYEVENTF_UNICODE, keys use the virtual key with its scan code.
type sendInputBackend struct{}

func (sendInputBackend) Begin() error { return nil }
func (sendInputBackend) End()         {}

func blocked(err error) error {
	var b win.ErrInputBlocked
	if errors.As(err, &b) {
		return protocol.Errorf(protocol.CodeIOFailed, "%v", err)
	}
	return err
}

// normalize maps a pixel coordinate inside [origin, origin+size) to the
// 0..65535 range so that Windows' conversion back lands on the same pixel.
func normalize(v, origin, size int32) int32 {
	if size <= 1 {
		return 0
	}
	return int32((int64(v-origin)*65536 + int64(size) - 1) / int64(size))
}

func (sendInputBackend) Move(x, y int) error {
	vs := win.VirtualScreen()
	w, h := vs.Right-vs.Left, vs.Bottom-vs.Top
	nx, ny := normalize(int32(x), vs.Left, w), normalize(int32(y), vs.Top, h)
	if err := win.SendMouse(nx, ny, 0, win.MouseMove|win.MouseAbsolute|win.MouseVirtualDesk); err != nil {
		return blocked(err)
	}
	// Rounding in the normalised space can land one pixel off on some
	// virtual-screen sizes; correct with SetCursorPos, which takes physical
	// coordinates directly in a Per-Monitor V2 aware process.
	if cx, cy, err := win.CursorPos(); err == nil && (cx != x || cy != y) {
		_ = win.SetCursorPos(x, y)
	}
	return nil
}

func (sendInputBackend) Position() (int, int, error) { return win.CursorPos() }

func (sendInputBackend) Button(b Button, down bool) error {
	var flag uint32
	switch b {
	case ButtonLeft:
		flag = win.MouseLeftUp
		if down {
			flag = win.MouseLeftDown
		}
	case ButtonRight:
		flag = win.MouseRightUp
		if down {
			flag = win.MouseRightDown
		}
	case ButtonMiddle:
		flag = win.MouseMiddleUp
		if down {
			flag = win.MouseMiddleDown
		}
	}
	return blocked(win.SendMouse(0, 0, 0, flag))
}

// Scroll sends one wheel event per tick; positive dy scrolls up (away from
// the user) and positive dx scrolls right, as in WM_MOUSEWHEEL/HWHEEL.
func (sendInputBackend) Scroll(dx, dy int) error {
	send := func(flag uint32, n int) error {
		step := int32(win.WheelDelta)
		if n < 0 {
			step, n = -step, -n
		}
		for i := 0; i < n; i++ {
			if err := win.SendMouse(0, 0, uint32(step), flag); err != nil {
				return blocked(err)
			}
		}
		return nil
	}
	if err := send(win.MouseWheel, dy); err != nil {
		return err
	}
	return send(win.MouseHWheel, dx)
}

func (sendInputBackend) Key(k Key, down bool) error {
	var flags uint32
	if k.Ext {
		flags |= win.KeyExtended
	}
	if !down {
		flags |= win.KeyUp
	}
	return blocked(win.SendKey(k.VK, k.Scan, flags))
}

func (sendInputBackend) TypeUnicode(r rune) error {
	units := utf16.Encode([]rune{r})
	for _, u := range units {
		if err := win.SendKey(0, u, win.KeyUnicode); err != nil {
			return blocked(err)
		}
	}
	for _, u := range units {
		if err := win.SendKey(0, u, win.KeyUnicode|win.KeyUp); err != nil {
			return blocked(err)
		}
	}
	return nil
}

// TypeScancode types an ASCII character with KEYEVENTF_SCANCODE events for
// applications that ignore Unicode injection, pressing Shift, Ctrl or Alt as
// the layout requires.
func (sendInputBackend) TypeScancode(r rune) error {
	vk, state, ok := win.VkKeyScan(r)
	if !ok {
		return protocol.Errorf(protocol.CodeIOFailed, "no key produces %q in the current keyboard layout", string(r))
	}
	type mod struct {
		bit uint8
		vk  uint16
	}
	mods := []mod{{1, 0xA0}, {2, 0xA2}, {4, 0xA4}}
	var pressed []uint16
	release := func() {
		for i := len(pressed) - 1; i >= 0; i-- {
			_ = win.SendKey(0, win.ScanCode(pressed[i]), win.KeyScancode|win.KeyUp)
		}
	}
	defer release()
	for _, m := range mods {
		if state&m.bit != 0 {
			if err := win.SendKey(0, win.ScanCode(m.vk), win.KeyScancode); err != nil {
				return blocked(err)
			}
			pressed = append(pressed, m.vk)
		}
	}
	scan := win.ScanCode(uint16(vk))
	if err := win.SendKey(0, scan, win.KeyScancode); err != nil {
		return blocked(err)
	}
	time.Sleep(time.Millisecond)
	return blocked(win.SendKey(0, scan, win.KeyScancode|win.KeyUp))
}
