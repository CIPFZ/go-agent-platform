//go:build linux

package input

import (
	"context"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/platform/wayland"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// waylandBackend injects input in a Wayland session (spec 13.3): through a
// uinput virtual device when /dev/uinput is writable, otherwise through the
// portal RemoteDesktop interface when an earlier approval left a restore
// token (as agreed). uinput sends evdev key codes, so text is limited to
// the US layout table; the portal sends keysyms and can type any character.
type waylandBackend struct {
	mu     sync.Mutex
	dev    *wayland.Device
	rd     *wayland.RemoteDesktop
	w, h   int
	px, py int
}

const setupTimeout = 15 * time.Second

func (b *waylandBackend) Begin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	w, h, err := wayland.ScreenSize(ctx)
	if err != nil {
		return err
	}
	b.w, b.h = w, h
	if wayland.UinputAccessible() {
		if b.dev != nil {
			if dw, dh := b.dev.Size(); dw == w && dh == h {
				return nil
			}
			b.dev.Close()
			b.dev = nil
		}
		dev, err := wayland.OpenDevice(w, h)
		if err != nil {
			return protocol.Errorf(protocol.CodeIOFailed, "create uinput device: %v", err)
		}
		b.dev = dev
		return nil
	}
	if b.rd != nil {
		return nil
	}
	if !wayland.TokenExists() {
		return protocol.Unsupported("Wayland input needs write access to %s (deploy/linux/preconditions.sh --system --wayland) or a stored RemoteDesktop approval (VM_AGENT_PORTAL_SETUP=1 vm-agent)", wayland.UinputPath)
	}
	rd, err := wayland.StartRemoteDesktop(ctx, true)
	if err != nil {
		return err
	}
	b.rd, b.px, b.py = rd, w/2, h/2
	return nil
}

func (b *waylandBackend) End() {}

func (b *waylandBackend) Move(x, y int) error {
	if x < 0 || y < 0 || x >= b.w || y >= b.h {
		return protocol.Errorf(protocol.CodeInvalidRequest, "(%d, %d) is outside the %dx%d desktop", x, y, b.w, b.h)
	}
	if b.dev != nil {
		return b.dev.Move(x, y)
	}
	b.px, b.py = x, y
	return b.rd.Move(x, y, b.w, b.h)
}

func (b *waylandBackend) Position() (int, int, error) {
	if b.dev != nil {
		x, y := b.dev.Position()
		return x, y, nil
	}
	return b.px, b.py, nil
}

var evdevButtons = map[Button]uint16{ButtonLeft: wayland.BtnLeft, ButtonRight: wayland.BtnRight, ButtonMiddle: wayland.BtnMiddle}

func (b *waylandBackend) Button(btn Button, down bool) error {
	if b.dev != nil {
		return b.dev.Key(evdevButtons[btn], down)
	}
	return b.rd.Button(evdevButtons[btn], down)
}

func (b *waylandBackend) Scroll(dx, dy int) error {
	if b.dev != nil {
		return b.dev.Wheel(dx, dy)
	}
	return b.rd.Wheel(dx, dy)
}

func (b *waylandBackend) Key(k Key, down bool) error {
	if b.dev != nil {
		return b.dev.Key(k.Evdev, down)
	}
	return b.rd.Key(k.Evdev, down)
}

func (b *waylandBackend) tapLayout(r rune) error {
	code, shift, ok := wayland.LayoutKey(r)
	if !ok {
		return protocol.Unsupported("%q cannot be typed through uinput; it covers the printable ASCII of a US layout", string(r))
	}
	press := func(c uint16, down bool) error {
		if b.dev != nil {
			return b.dev.Key(c, down)
		}
		return b.rd.Key(c, down)
	}
	if shift {
		if err := press(wayland.KeyShift, true); err != nil {
			return err
		}
		defer press(wayland.KeyShift, false)
	}
	if err := press(code, true); err != nil {
		return err
	}
	return press(code, false)
}

func (b *waylandBackend) TypeUnicode(r rune) error {
	if b.dev != nil {
		return b.tapLayout(r)
	}
	sym := uint32(r)
	if r > 0xff {
		sym = 0x01000000 | uint32(r)
	}
	if err := b.rd.Keysym(sym, true); err != nil {
		return err
	}
	return b.rd.Keysym(sym, false)
}

func (b *waylandBackend) TypeScancode(r rune) error { return b.tapLayout(r) }
