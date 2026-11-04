//go:build windows

package clipboard

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/png"
	"runtime"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend {
	b := &win32Backend{requests: make(chan func(), 1)}
	go b.loop()
	return b
}

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procEnumClipboardFormats       = user32.NewProc("EnumClipboardFormats")
	procGetClipboardFormatNameW    = user32.NewProc("GetClipboardFormatNameW")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
)

const (
	cfDIB         = 8
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
	hwndMessage   = ^uintptr(2) // (HWND)-3
)

var standardFormats = map[uint32]string{
	1: "CF_TEXT", 2: "CF_BITMAP", 3: "CF_METAFILEPICT", 4: "CF_SYLK", 5: "CF_DIF", 6: "CF_TIFF", 7: "CF_OEMTEXT",
	8: "CF_DIB", 9: "CF_PALETTE", 10: "CF_PENDATA", 11: "CF_RIFF", 12: "CF_WAVE", 13: "CF_UNICODETEXT",
	14: "CF_ENHMETAFILE", 15: "CF_HDROP", 16: "CF_LOCALE", 17: "CF_DIBV5",
}

// win32Backend runs every clipboard call on one locked OS thread that owns a
// message-only window, which EmptyClipboard needs as the clipboard owner
// for SetClipboardData to succeed (spec section 10).
type win32Backend struct {
	requests chan func()
	hwnd     uintptr
	pngFmt   uint32
	initErr  error
}

func (b *win32Backend) loop() {
	runtime.LockOSThread()
	class, _ := windows.UTF16PtrFromString("STATIC")
	b.hwnd, _, b.initErr = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, 0, 0)
	if b.hwnd != 0 {
		b.initErr = nil
	}
	name, _ := windows.UTF16PtrFromString("PNG")
	f, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
	b.pngFmt = uint32(f)
	for fn := range b.requests {
		fn()
	}
}

// run executes fn on the clipboard thread; ctx bounds only the wait for
// the thread, as clipboard calls themselves return promptly.
func (b *win32Backend) run(ctx context.Context, fn func() error) error {
	done := make(chan error, 1)
	select {
	case b.requests <- func() { done <- fn() }:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// open retries OpenClipboard, which fails while another program has the
// clipboard open.
func (b *win32Backend) open(ctx context.Context) error {
	if b.hwnd == 0 {
		return protocol.Errorf(protocol.CodeIOFailed, "create clipboard window: %v", b.initErr)
	}
	for {
		if r, _, err := procOpenClipboard.Call(b.hwnd); r != 0 {
			return nil
		} else if ctx.Err() != nil {
			return protocol.Errorf(protocol.CodeIOFailed, "the clipboard is held open by another program: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func closeClipboard() { procCloseClipboard.Call() }

func (b *win32Backend) Read(ctx context.Context) (Content, error) {
	var c Content
	err := b.run(ctx, func() error {
		if err := b.open(ctx); err != nil {
			return err
		}
		defer closeClipboard()
		c.Formats = []string{}
		for f := uint32(0); ; {
			r, _, _ := procEnumClipboardFormats.Call(uintptr(f))
			if r == 0 {
				break
			}
			f = uint32(r)
			c.Formats = append(c.Formats, formatName(f))
		}
		if data, ok := getData(cfUnicodeText); ok {
			u := make([]uint16, len(data)/2)
			for i := range u {
				u[i] = binary.LittleEndian.Uint16(data[i*2:])
			}
			for len(u) > 0 && u[len(u)-1] == 0 {
				u = u[:len(u)-1]
			}
			text := string(utf16.Decode(u))
			c.Text = &text
		}
		if data, ok := getData(b.pngFmt); ok && b.pngFmt != 0 {
			c.PNG = data
		} else if data, ok := getData(cfDIB); ok {
			if img, err := dibToImage(data); err == nil {
				var buf bytes.Buffer
				if png.Encode(&buf, img) == nil {
					c.PNG = buf.Bytes()
				}
			}
		}
		return nil
	})
	return c, err
}

func formatName(f uint32) string {
	if name, ok := standardFormats[f]; ok {
		return name
	}
	buf := make([]uint16, 256)
	n, _, _ := procGetClipboardFormatNameW.Call(uintptr(f), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return "0x" + hex16(f)
	}
	return windows.UTF16ToString(buf[:n])
}

func hex16(v uint32) string {
	const digits = "0123456789abcdef"
	out := []byte{}
	for i := 12; i >= 0; i -= 4 {
		out = append(out, digits[(v>>uint(i))&0xf])
	}
	return string(out)
}

// getData copies the clipboard data of format f.
func getData(f uint32) ([]byte, bool) {
	if f == 0 {
		return nil, false
	}
	if r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(f)); r == 0 {
		return nil, false
	}
	h, _, _ := procGetClipboardData.Call(uintptr(f))
	if h == 0 {
		return nil, false
	}
	size, _, _ := procGlobalSize.Call(h)
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 || size == 0 || size > MaxBytes {
		if ptr != 0 {
			procGlobalUnlock.Call(h)
		}
		return nil, false
	}
	defer procGlobalUnlock.Call(h)
	out := make([]byte, size)
	copy(out, unsafe.Slice((*byte)(foreign(ptr)), size))
	return out, true
}

// setData hands a copy of data to the clipboard as format f; on success
// the clipboard owns the memory.
func setData(f uint32, data []byte) error {
	h, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if h == 0 {
		return err
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(foreign(ptr)), len(data)), data)
	procGlobalUnlock.Call(h)
	if r, _, err := procSetClipboardData.Call(uintptr(f), h); r == 0 {
		procGlobalFree.Call(h)
		return err
	}
	return nil
}

func (b *win32Backend) write(ctx context.Context, items map[uint32][]byte) error {
	return b.run(ctx, func() error {
		if err := b.open(ctx); err != nil {
			return err
		}
		defer closeClipboard()
		if r, _, err := procEmptyClipboard.Call(); r == 0 {
			return protocol.Errorf(protocol.CodeIOFailed, "EmptyClipboard: %v", err)
		}
		for f, data := range items {
			if err := setData(f, data); err != nil {
				return protocol.Errorf(protocol.CodeIOFailed, "SetClipboardData: %v", err)
			}
		}
		return nil
	})
}

func (b *win32Backend) WriteText(ctx context.Context, text string) error {
	u := utf16.Encode([]rune(text))
	data := make([]byte, 2*len(u)+2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	return b.write(ctx, map[uint32][]byte{cfUnicodeText: data})
}

// WritePNG places the image both as the registered "PNG" format (alpha
// preserved) and as CF_DIB for applications that only read bitmaps.
func (b *win32Backend) WritePNG(ctx context.Context, data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "image must be a PNG: %v", err)
	}
	items := map[uint32][]byte{cfDIB: imageToDIB(img)}
	if b.pngFmt != 0 {
		items[b.pngFmt] = data
	}
	return b.write(ctx, items)
}

// foreign converts an address of memory not managed by Go (a GlobalLock
// result) to a pointer; the indirection keeps vet's uintptr check quiet for
// this legitimate case.
func foreign(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }
