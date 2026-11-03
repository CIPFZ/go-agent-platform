//go:build windows

package win

import (
	"errors"
	"image"
	"unsafe"

	"golang.org/x/sys/windows"
)

type cursorInfo struct {
	cbSize      uint32
	flags       uint32
	hCursor     windows.Handle
	ptScreenPos POINT
}

const cursorShowing = 1

// CursorState returns the pointer position, visibility and shape handle.
func CursorState() (x, y int, visible bool, handle windows.Handle, err error) {
	ci := cursorInfo{}
	ci.cbSize = uint32(unsafe.Sizeof(ci))
	if r, callErr := Call(procGetCursorInfo, uintptr(unsafe.Pointer(&ci))); r == 0 {
		return 0, 0, false, 0, lastErr(callErr)
	}
	return int(ci.ptScreenPos.X), int(ci.ptScreenPos.Y), ci.flags&cursorShowing != 0, ci.hCursor, nil
}

// CursorPos returns the pointer position.
func CursorPos() (int, int, error) {
	var p POINT
	if r, err := Call(procGetCursorPos, uintptr(unsafe.Pointer(&p))); r == 0 {
		return 0, 0, lastErr(err)
	}
	return int(p.X), int(p.Y), nil
}

// SetCursorPos moves the pointer directly.
func SetCursorPos(x, y int) error {
	if r, err := Call(procSetCursorPos, uintptr(int32(x)), uintptr(int32(y))); r == 0 {
		return lastErr(err)
	}
	return nil
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  windows.Handle
	hbmColor windows.Handle
}

type bitmap struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

const (
	diNormal    = 0x0003
	blackBrush  = 4
	whiteBrush  = 0
	dibRGBColor = 0
)

// CursorImage renders the cursor h to RGBA with straight alpha recovered by
// drawing it on black and on white backgrounds, which also handles
// monochrome and inverting cursors approximately (spec section 10).
func CursorImage(h windows.Handle) (*image.RGBA, image.Point, error) {
	var ii iconInfo
	if r, err := Call(procGetIconInfo, uintptr(h), uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil, image.Point{}, lastErr(err)
	}
	defer Call(procDeleteObject, uintptr(ii.hbmMask))
	if ii.hbmColor != 0 {
		defer Call(procDeleteObject, uintptr(ii.hbmColor))
	}
	var bm bitmap
	if r, _ := Call(procGetObjectW, uintptr(ii.hbmMask), unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil, image.Point{}, errors.New("GetObject failed for cursor mask")
	}
	w, height := int(bm.bmWidth), int(bm.bmHeight)
	if ii.hbmColor == 0 {
		height /= 2 // monochrome cursors stack the AND and XOR masks
	}
	if w <= 0 || height <= 0 || w > 512 || height > 512 {
		return nil, image.Point{}, errors.New("unexpected cursor size")
	}
	onBlack, err := renderCursor(h, w, height, blackBrush)
	if err != nil {
		return nil, image.Point{}, err
	}
	onWhite, err := renderCursor(h, w, height, whiteBrush)
	if err != nil {
		return nil, image.Point{}, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, height))
	for i := 0; i < w*height; i++ {
		// DIB pixels are BGRA.
		b0, g0, r0 := int(onBlack[i*4]), int(onBlack[i*4+1]), int(onBlack[i*4+2])
		g1 := int(onWhite[i*4+1])
		alpha := 255 - (g1 - g0)
		alpha = min(max(alpha, 0), 255)
		// Premultiplied colour is exactly the render on black.
		img.Pix[i*4+0] = uint8(min(r0, alpha))
		img.Pix[i*4+1] = uint8(min(g0, alpha))
		img.Pix[i*4+2] = uint8(min(b0, alpha))
		img.Pix[i*4+3] = uint8(alpha)
	}
	return img, image.Pt(int(ii.xHotspot), int(ii.yHotspot)), nil
}

func renderCursor(h windows.Handle, w, height int, brush uintptr) ([]byte, error) {
	screenDC, _ := Call(procGetDC, 0)
	if screenDC == 0 {
		return nil, errors.New("GetDC failed")
	}
	defer Call(procReleaseDC, 0, screenDC)
	dc, _ := Call(procCreateCompatibleDC, screenDC)
	if dc == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	defer Call(procDeleteDC, dc)
	hdr := bitmapInfoHeader{biWidth: int32(w), biHeight: -int32(height), biPlanes: 1, biBitCount: 32}
	hdr.biSize = uint32(unsafe.Sizeof(hdr))
	var bits unsafe.Pointer // set by CreateDIBSection to memory owned by the DIB
	dib, _ := Call(procCreateDIBSection, dc, uintptr(unsafe.Pointer(&hdr)), dibRGBColor, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == nil {
		return nil, errors.New("CreateDIBSection failed")
	}
	defer Call(procDeleteObject, dib)
	old, _ := Call(procSelectObject, dc, dib)
	defer Call(procSelectObject, dc, old)
	rect := RECT{Right: int32(w), Bottom: int32(height)}
	stock, _ := Call(procGetStockObject, brush)
	Call(procFillRect, dc, uintptr(unsafe.Pointer(&rect)), stock)
	if r, err := Call(procDrawIconEx, dc, 0, 0, uintptr(h), uintptr(w), uintptr(height), 0, 0, diNormal); r == 0 {
		return nil, lastErr(err)
	}
	out := make([]byte, w*height*4)
	copy(out, unsafe.Slice((*byte)(bits), len(out)))
	return out, nil
}
