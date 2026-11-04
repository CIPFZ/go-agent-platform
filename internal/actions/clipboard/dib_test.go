package clipboard

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestDIBRoundTrip(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	src.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	src.SetNRGBA(2, 1, color.NRGBA{0, 0, 255, 128})
	dib := imageToDIB(src)
	if binary.LittleEndian.Uint32(dib[0:]) != 40 || len(dib) != 40+3*2*4 {
		t.Fatalf("header/size %d", len(dib))
	}
	// Bottom-up: the first stored row is the image's last row.
	if dib[40+2*4+0] != 255 || dib[40+2*4+3] != 128 {
		t.Fatalf("bottom-up pixel = %v", dib[40+8:40+12])
	}
	back, err := dibToImage(dib)
	if err != nil {
		t.Fatal(err)
	}
	if back.At(0, 0) != (color.NRGBA{255, 0, 0, 255}) || back.At(2, 1) != (color.NRGBA{0, 0, 255, 128}) {
		t.Fatalf("round trip: %v %v", back.At(0, 0), back.At(2, 1))
	}
}

func TestDIB24BitTopDownAndZeroAlpha(t *testing.T) {
	w, h := 2, 2
	stride := 8 // 2*3 bytes padded to 4
	dib := make([]byte, 40+stride*h)
	binary.LittleEndian.PutUint32(dib[0:], 40)
	binary.LittleEndian.PutUint32(dib[4:], uint32(w))
	binary.LittleEndian.PutUint32(dib[8:], uint32(0xffffffff-uint32(h)+1)) // -2: top-down
	binary.LittleEndian.PutUint16(dib[12:], 1)
	binary.LittleEndian.PutUint16(dib[14:], 24)
	copy(dib[40:], []byte{0, 0, 255, 0, 255, 0}) // row 0: red, green (BGR)
	img, err := dibToImage(dib)
	if err != nil {
		t.Fatal(err)
	}
	if img.At(0, 0) != (color.NRGBA{255, 0, 0, 255}) || img.At(1, 0) != (color.NRGBA{0, 255, 0, 255}) {
		t.Fatalf("24-bit top-down = %v %v", img.At(0, 0), img.At(1, 0))
	}
	zero := imageToDIB(image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	if img, _ := dibToImage(zero); img.At(0, 0).(color.NRGBA).A != 255 {
		t.Fatal("all-zero alpha must read as opaque")
	}
	if _, err := dibToImage([]byte{1, 2, 3}); err == nil {
		t.Fatal("short DIB accepted")
	}
}
