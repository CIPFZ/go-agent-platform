package clipboard

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
)

// imageToDIB renders img as a bottom-up 32-bit BI_RGB DIB (header plus
// BGRA pixels, alpha unpremultiplied as the format conventionally holds).
func imageToDIB(img image.Image) []byte {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	out := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(out[0:], 40)
	binary.LittleEndian.PutUint32(out[4:], uint32(int32(w)))
	binary.LittleEndian.PutUint32(out[8:], uint32(int32(h)))
	binary.LittleEndian.PutUint16(out[12:], 1)
	binary.LittleEndian.PutUint16(out[14:], 32)
	binary.LittleEndian.PutUint32(out[20:], uint32(w*h*4))
	px := out[40:]
	for y := 0; y < h; y++ {
		row := px[(h-1-y)*w*4:]
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			row[x*4+0], row[x*4+1], row[x*4+2], row[x*4+3] = c.B, c.G, c.R, c.A
		}
	}
	return out
}

// dibToImage decodes a CF_DIB: BITMAPINFOHEADER (or a V4/V5 header) with
// 24 or 32 bits per pixel, BI_RGB or BI_BITFIELDS, bottom-up or top-down.
// 32-bit DIBs whose alpha bytes are all zero are treated as opaque.
func dibToImage(data []byte) (image.Image, error) {
	if len(data) < 40 {
		return nil, errors.New("short DIB")
	}
	hdr := binary.LittleEndian.Uint32(data[0:])
	w := int(int32(binary.LittleEndian.Uint32(data[4:])))
	hRaw := int(int32(binary.LittleEndian.Uint32(data[8:])))
	bpp := int(binary.LittleEndian.Uint16(data[14:]))
	compression := binary.LittleEndian.Uint32(data[16:])
	if w <= 0 || hRaw == 0 || (bpp != 24 && bpp != 32) || (compression != 0 && compression != 3) || hdr < 40 {
		return nil, errors.New("unsupported DIB")
	}
	topDown := hRaw < 0
	h := hRaw
	if topDown {
		h = -h
	}
	offset := int(hdr)
	if compression == 3 && hdr == 40 {
		offset += 12 // three DWORD colour masks follow the basic header
	}
	stride := ((w*bpp + 31) / 32) * 4
	if len(data) < offset+stride*h {
		return nil, errors.New("truncated DIB")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for y := 0; y < h; y++ {
		src := y
		if !topDown {
			src = h - 1 - y
		}
		row := data[offset+src*stride:]
		for x := 0; x < w; x++ {
			i := x * (bpp / 8)
			a := byte(255)
			if bpp == 32 {
				a = row[i+3]
				if a != 0 {
					anyAlpha = true
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{R: row[i+2], G: row[i+1], B: row[i], A: a})
		}
	}
	if bpp == 32 && !anyAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	return img, nil
}
