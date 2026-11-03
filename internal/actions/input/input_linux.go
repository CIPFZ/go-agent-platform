//go:build linux

package input

import (
	"sync"
	"time"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend { return &xtestBackend{} }

// X core event types used with XTest FakeInput.
const (
	xKeyPress      = 2
	xKeyRelease    = 3
	xButtonPress   = 4
	xButtonRelease = 5
	xMotionNotify  = 6
)

// remapSettle gives clients time to process the MappingNotify caused by a
// temporary key binding before the key event arrives.
const remapSettle = 15 * time.Millisecond

// xtestBackend injects input with the XTest extension. Characters without a
// key in the current layout are bound to a spare keycode for the duration of
// the request (spec section 10).
type xtestBackend struct {
	mu      sync.Mutex
	c       *x11.Conn
	ox, oy  int
	minKC   xproto.Keycode
	perKC   int
	syms    []xproto.Keysym
	spare   []xproto.Keycode
	next    int
	bound   map[xproto.Keysym]xproto.Keycode
	shiftKC xproto.Keycode
}

func (b *xtestBackend) Begin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, err := x11.Shared()
	if err != nil {
		return protocol.Errorf(protocol.CodeNoDisplay, "%v", err)
	}
	if !c.HasXTest {
		return protocol.Unsupported("the X server lacks the XTest extension")
	}
	b.c = c
	if b.ox, b.oy, err = c.Origin(); err != nil {
		return err
	}
	return b.loadMapping()
}

func (b *xtestBackend) loadMapping() error {
	minKC, maxKC := b.c.Setup.MinKeycode, b.c.Setup.MaxKeycode
	reply, err := xproto.GetKeyboardMapping(b.c.X, minKC, byte(maxKC-minKC+1)).Reply()
	if err != nil {
		return b.c.Check(err)
	}
	b.minKC, b.perKC, b.syms = minKC, int(reply.KeysymsPerKeycode), reply.Keysyms
	b.spare, b.next, b.bound = nil, 0, map[xproto.Keysym]xproto.Keycode{}
	for kc := int(maxKC); kc >= int(minKC); kc-- {
		empty := true
		for col := 0; col < b.perKC; col++ {
			if b.sym(xproto.Keycode(kc), col) != 0 {
				empty = false
				break
			}
		}
		if empty {
			b.spare = append(b.spare, xproto.Keycode(kc))
		}
	}
	b.shiftKC, _ = b.find(0xffe1)
	return nil
}

func (b *xtestBackend) sym(kc xproto.Keycode, col int) xproto.Keysym {
	i := int(kc-b.minKC)*b.perKC + col
	if col >= b.perKC || i < 0 || i >= len(b.syms) {
		return 0
	}
	return b.syms[i]
}

// find returns the keycode producing keysym and whether Shift is needed,
// preferring unshifted positions. ok is false when no key produces it.
func (b *xtestBackend) find(keysym uint32) (xproto.Keycode, bool) {
	kc, _, ok := b.lookup(keysym)
	return kc, ok
}

func (b *xtestBackend) lookup(keysym uint32) (kc xproto.Keycode, shift bool, ok bool) {
	if kc, ok := b.bound[xproto.Keysym(keysym)]; ok {
		return kc, false, true
	}
	n := len(b.syms) / max(b.perKC, 1)
	for col := 0; col < 2; col++ {
		for i := 0; i < n; i++ {
			k := b.minKC + xproto.Keycode(i)
			if uint32(b.sym(k, col)) == keysym {
				return k, col == 1, true
			}
		}
	}
	return 0, false, false
}

// bind maps keysym to a spare keycode on every column so the Shift state
// does not matter.
func (b *xtestBackend) bind(keysym uint32) (xproto.Keycode, error) {
	if len(b.spare) == 0 {
		return 0, protocol.Errorf(protocol.CodeIOFailed, "no spare keycode is free to type keysym 0x%x", keysym)
	}
	kc := b.spare[b.next%len(b.spare)]
	b.next++
	for sym, k := range b.bound {
		if k == kc {
			delete(b.bound, sym)
		}
	}
	syms := make([]xproto.Keysym, b.perKC)
	for i := range syms {
		syms[i] = xproto.Keysym(keysym)
	}
	if err := xproto.ChangeKeyboardMappingChecked(b.c.X, 1, kc, byte(b.perKC), syms).Check(); err != nil {
		return 0, b.c.Check(err)
	}
	if err := b.c.Sync(); err != nil {
		return 0, err
	}
	time.Sleep(remapSettle)
	b.bound[xproto.Keysym(keysym)] = kc
	return kc, nil
}

func (b *xtestBackend) End() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c == nil {
		return
	}
	if len(b.bound) > 0 {
		// Let the last key events be processed before their keycodes lose
		// the temporary symbol.
		_ = b.c.Sync()
		time.Sleep(remapSettle)
		empty := make([]xproto.Keysym, b.perKC)
		for _, kc := range b.bound {
			_ = xproto.ChangeKeyboardMappingChecked(b.c.X, 1, kc, byte(b.perKC), empty).Check()
		}
		b.bound = map[xproto.Keysym]xproto.Keycode{}
	}
	_ = b.c.Sync()
}

func (b *xtestBackend) fake(typ byte, detail byte, x, y int16) error {
	if err := xtest.FakeInputChecked(b.c.X, typ, detail, 0, b.c.Root, x, y, 0).Check(); err != nil {
		return b.c.Check(err)
	}
	return nil
}

func (b *xtestBackend) Move(x, y int) error {
	rx, ry := x+b.ox, y+b.oy
	if rx < -32768 || rx > 32767 || ry < -32768 || ry > 32767 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "coordinate (%d, %d) is out of range", x, y)
	}
	return b.fake(xMotionNotify, 0, int16(rx), int16(ry))
}

func (b *xtestBackend) Position() (int, int, error) {
	x, y, err := b.c.Pointer()
	return x - b.ox, y - b.oy, err
}

var xButtons = map[Button]byte{ButtonLeft: 1, ButtonMiddle: 2, ButtonRight: 3}

func (b *xtestBackend) Button(btn Button, down bool) error {
	typ := byte(xButtonRelease)
	if down {
		typ = xButtonPress
	}
	return b.fake(typ, xButtons[btn], 0, 0)
}

// Scroll uses the core wheel buttons: 4 up, 5 down, 6 left, 7 right.
func (b *xtestBackend) Scroll(dx, dy int) error {
	click := func(button byte, n int) error {
		for i := 0; i < n; i++ {
			if err := b.fake(xButtonPress, button, 0, 0); err != nil {
				return err
			}
			if err := b.fake(xButtonRelease, button, 0, 0); err != nil {
				return err
			}
		}
		return nil
	}
	if dy > 0 {
		if err := click(4, dy); err != nil {
			return err
		}
	} else if err := click(5, -dy); err != nil {
		return err
	}
	if dx > 0 {
		return click(7, dx)
	}
	return click(6, -dx)
}

func (b *xtestBackend) keycode(keysym uint32) (xproto.Keycode, error) {
	if kc, ok := b.find(keysym); ok {
		return kc, nil
	}
	return b.bind(keysym)
}

func (b *xtestBackend) Key(k Key, down bool) error {
	kc, err := b.keycode(k.Keysym)
	if err != nil {
		return err
	}
	typ := byte(xKeyRelease)
	if down {
		typ = xKeyPress
	}
	return b.fake(typ, byte(kc), 0, 0)
}

func (b *xtestBackend) tap(kc xproto.Keycode, shift bool) error {
	if shift {
		if b.shiftKC == 0 {
			return protocol.Errorf(protocol.CodeIOFailed, "no Shift key in the current keyboard mapping")
		}
		if err := b.fake(xKeyPress, byte(b.shiftKC), 0, 0); err != nil {
			return err
		}
		defer b.fake(xKeyRelease, byte(b.shiftKC), 0, 0)
	}
	if err := b.fake(xKeyPress, byte(kc), 0, 0); err != nil {
		return err
	}
	return b.fake(xKeyRelease, byte(kc), 0, 0)
}

// runeKeysym returns the X keysym of r: Latin-1 maps directly, everything
// else uses the Unicode keysym range 0x01000000 + code point.
func runeKeysym(r rune) uint32 {
	if (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff) {
		return uint32(r)
	}
	return 0x01000000 | uint32(r)
}

func (b *xtestBackend) TypeUnicode(r rune) error {
	sym := runeKeysym(r)
	if kc, shift, ok := b.lookup(sym); ok {
		return b.tap(kc, shift)
	}
	kc, err := b.bind(sym)
	if err != nil {
		return err
	}
	return b.tap(kc, false)
}

// TypeScancode types r using only keys of the current layout, never a
// temporary binding.
func (b *xtestBackend) TypeScancode(r rune) error {
	kc, shift, ok := b.lookup(runeKeysym(r))
	if !ok {
		return protocol.Errorf(protocol.CodeIOFailed, "no key produces %q in the current keyboard layout", string(r))
	}
	return b.tap(kc, shift)
}
