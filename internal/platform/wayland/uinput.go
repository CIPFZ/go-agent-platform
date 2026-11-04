//go:build linux

package wayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// linux/uinput.h and linux/input-event-codes.h.
const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	synReport = 0
	relHWheel = 0x06
	relWheel  = 0x08
	absX      = 0x00
	absY      = 0x01

	BtnLeft   = 0x110
	BtnRight  = 0x111
	BtnMiddle = 0x112

	busVirtual = 0x06
)

func ioc(dir, typ, nr, size uintptr) uintptr { return dir<<30 | size<<16 | typ<<8 | nr }

var (
	uiDevCreate  = ioc(0, 'U', 1, 0)
	uiDevDestroy = ioc(0, 'U', 2, 0)
	uiDevSetup   = ioc(1, 'U', 3, unsafe.Sizeof(uinputSetup{}))
	uiAbsSetup   = ioc(1, 'U', 4, unsafe.Sizeof(uinputAbsSetup{}))
	uiSetEvBit   = ioc(1, 'U', 100, 4)
	uiSetKeyBit  = ioc(1, 'U', 101, 4)
	uiSetRelBit  = ioc(1, 'U', 102, 4)
	uiSetAbsBit  = ioc(1, 'U', 103, 4)
)

type inputID struct{ Bustype, Vendor, Product, Version uint16 }

type uinputSetup struct {
	ID           inputID
	Name         [80]byte
	FFEffectsMax uint32
}

type absInfo struct{ Value, Minimum, Maximum, Fuzz, Flat, Resolution int32 }

type uinputAbsSetup struct {
	Code uint16
	_    uint16
	Info absInfo
}

// UinputPath is the uinput device node.
const UinputPath = "/dev/uinput"

// Device is a virtual absolute pointer plus keyboard. Compositors treat an
// absolute pointer (like a VM tablet) as covering the whole output layout,
// so the axis range is the desktop size and coordinates are pixels.
type Device struct {
	mu   sync.Mutex
	f    *os.File
	w, h int
	x, y int
}

// UinputAccessible reports whether /dev/uinput can be opened for writing.
func UinputAccessible() bool { return unix.Access(UinputPath, unix.W_OK) == nil }

func ioctl(fd uintptr, req uintptr, arg uintptr) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

// OpenDevice creates the virtual device for a w x h desktop. It enables
// every key code below 0x300 so any key of the table can be sent.
func OpenDevice(w, h int) (*Device, error) {
	f, err := os.OpenFile(UinputPath, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	fail := func(err error) (*Device, error) {
		f.Close()
		return nil, fmt.Errorf("configure uinput device: %w", err)
	}
	for _, ev := range []uintptr{evKey, evRel, evAbs, evSyn} {
		if err := ioctl(fd, uiSetEvBit, ev); err != nil {
			return fail(err)
		}
	}
	for code := uintptr(1); code < 0x2ff; code++ {
		if code >= 0x100 && code < 0x110 { // BTN_MISC range: not a keyboard or mouse
			continue
		}
		if code >= 0x113 && code < 0x160 { // other mouse, joystick and tablet buttons
			continue
		}
		if err := ioctl(fd, uiSetKeyBit, code); err != nil {
			return fail(err)
		}
	}
	for _, rel := range []uintptr{relWheel, relHWheel} {
		if err := ioctl(fd, uiSetRelBit, rel); err != nil {
			return fail(err)
		}
	}
	for _, axis := range []struct {
		code uint16
		max  int
	}{{absX, w - 1}, {absY, h - 1}} {
		if err := ioctl(fd, uiSetAbsBit, uintptr(axis.code)); err != nil {
			return fail(err)
		}
		as := uinputAbsSetup{Code: axis.code, Info: absInfo{Maximum: int32(axis.max)}}
		if err := ioctl(fd, uiAbsSetup, uintptr(unsafe.Pointer(&as))); err != nil {
			return fail(err)
		}
	}
	setup := uinputSetup{ID: inputID{Bustype: busVirtual, Vendor: 0x1d6b, Product: 0x7661, Version: 1}}
	copy(setup.Name[:], "vm-agent virtual input")
	if err := ioctl(fd, uiDevSetup, uintptr(unsafe.Pointer(&setup))); err != nil {
		return fail(err)
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		return fail(err)
	}
	// The compositor needs a moment to pick up the new device through udev.
	time.Sleep(500 * time.Millisecond)
	return &Device{f: f, w: w, h: h, x: w / 2, y: h / 2}, nil
}

// Size returns the axis ranges the device was created for.
func (d *Device) Size() (int, int) { return d.w, d.h }

// Close destroys the virtual device.
func (d *Device) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f != nil {
		_ = ioctl(d.f.Fd(), uiDevDestroy, 0)
		d.f.Close()
		d.f = nil
	}
}

// input_event on 64-bit Linux: struct timeval (16 bytes), type, code, value.
func (d *Device) emit(events ...[3]int32) error {
	buf := make([]byte, 0, 24*(len(events)+1))
	put := func(typ, code uint16, value int32) {
		var e [24]byte
		binary.LittleEndian.PutUint16(e[16:], typ)
		binary.LittleEndian.PutUint16(e[18:], code)
		binary.LittleEndian.PutUint32(e[20:], uint32(value))
		buf = append(buf, e[:]...)
	}
	for _, e := range events {
		put(uint16(e[0]), uint16(e[1]), e[2])
	}
	put(evSyn, synReport, 0)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return errors.New("uinput device is closed")
	}
	_, err := d.f.Write(buf)
	return err
}

// Move warps the pointer to desktop pixel (x, y), clamped to the range.
func (d *Device) Move(x, y int) error {
	x, y = min(max(x, 0), d.w-1), min(max(y, 0), d.h-1)
	if err := d.emit([3]int32{evAbs, absX, int32(x)}, [3]int32{evAbs, absY, int32(y)}); err != nil {
		return err
	}
	d.mu.Lock()
	d.x, d.y = x, y
	d.mu.Unlock()
	return nil
}

// Position returns the last position set through the device.
func (d *Device) Position() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.x, d.y
}

// Key presses or releases an evdev key or button code.
func (d *Device) Key(code uint16, down bool) error {
	v := int32(0)
	if down {
		v = 1
	}
	return d.emit([3]int32{evKey, int32(code), v})
}

// Wheel scrolls by detents; positive dy is up, positive dx right.
func (d *Device) Wheel(dx, dy int) error {
	var events [][3]int32
	if dy != 0 {
		events = append(events, [3]int32{evRel, relWheel, int32(dy)})
	}
	if dx != 0 {
		events = append(events, [3]int32{evRel, relHWheel, int32(dx)})
	}
	if len(events) == 0 {
		return nil
	}
	return d.emit(events...)
}
