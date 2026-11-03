// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package jpeg is a baseline JPEG encoder for *image.RGBA derived from the Go
// standard library's image/jpeg writer. It produces the same 4:2:0 baseline
// stream with the standard tables, but splits the image into horizontal
// strips of MCU rows separated by restart markers (DRI/RSTn) and entropy-codes
// the strips on parallel goroutines. Restart intervals reset DC prediction,
// so every strip is independent and the output is an ordinary baseline JPEG
// that any decoder accepts. A 1080p desktop capture is encoded several times
// faster than with image/jpeg on a multi-core VM, which is what keeps
// /v1/screen/capture inside the latency budget of spec 13.1.
package jpeg

import (
	"errors"
	"image"
	"io"
	"runtime"
	"sync"
)

func div(a, b int32) int32 {
	if a >= 0 {
		return (a + (b >> 1)) / b
	}
	return -((-a + (b >> 1)) / b)
}

var bitCount = [256]byte{
	0, 1, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 4, 4, 4, 4,
	5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5,
	6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
	6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6, 6,
	7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
	7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
	7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
	7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
	8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8,
}

// unscaledQuant are the unscaled quantization tables in zig-zag order
// (section K.1 of the JPEG spec).
var unscaledQuant = [2][blockSize]byte{
	{
		16, 11, 12, 14, 12, 10, 16, 14,
		13, 14, 18, 17, 16, 19, 24, 40,
		26, 24, 22, 22, 24, 49, 35, 37,
		29, 40, 58, 51, 61, 60, 57, 51,
		56, 55, 64, 72, 92, 78, 64, 68,
		87, 69, 55, 56, 80, 109, 81, 87,
		95, 98, 103, 104, 103, 62, 77, 113,
		121, 112, 100, 120, 92, 101, 103, 99,
	},
	{
		17, 18, 18, 24, 21, 24, 47, 26,
		26, 47, 99, 66, 56, 66, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99,
	},
}

type huffmanSpec struct {
	count [16]byte
	value []byte
}

// theHuffmanSpec are the standard tables of section K.3: luminance DC,
// luminance AC, chrominance DC, chrominance AC.
var theHuffmanSpec = [4]huffmanSpec{
	{
		[16]byte{0, 1, 5, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0},
		[]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
	},
	{
		[16]byte{0, 2, 1, 3, 3, 2, 4, 3, 5, 5, 4, 4, 0, 0, 1, 125},
		[]byte{
			0x01, 0x02, 0x03, 0x00, 0x04, 0x11, 0x05, 0x12,
			0x21, 0x31, 0x41, 0x06, 0x13, 0x51, 0x61, 0x07,
			0x22, 0x71, 0x14, 0x32, 0x81, 0x91, 0xa1, 0x08,
			0x23, 0x42, 0xb1, 0xc1, 0x15, 0x52, 0xd1, 0xf0,
			0x24, 0x33, 0x62, 0x72, 0x82, 0x09, 0x0a, 0x16,
			0x17, 0x18, 0x19, 0x1a, 0x25, 0x26, 0x27, 0x28,
			0x29, 0x2a, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39,
			0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49,
			0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59,
			0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69,
			0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79,
			0x7a, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89,
			0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98,
			0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
			0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6,
			0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3, 0xc4, 0xc5,
			0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4,
			0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda, 0xe1, 0xe2,
			0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea,
			0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
			0xf9, 0xfa,
		},
	},
	{
		[16]byte{0, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0},
		[]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
	},
	{
		[16]byte{0, 2, 1, 2, 4, 4, 3, 4, 7, 5, 4, 4, 0, 1, 2, 119},
		[]byte{
			0x00, 0x01, 0x02, 0x03, 0x11, 0x04, 0x05, 0x21,
			0x31, 0x06, 0x12, 0x41, 0x51, 0x07, 0x61, 0x71,
			0x13, 0x22, 0x32, 0x81, 0x08, 0x14, 0x42, 0x91,
			0xa1, 0xb1, 0xc1, 0x09, 0x23, 0x33, 0x52, 0xf0,
			0x15, 0x62, 0x72, 0xd1, 0x0a, 0x16, 0x24, 0x34,
			0xe1, 0x25, 0xf1, 0x17, 0x18, 0x19, 0x1a, 0x26,
			0x27, 0x28, 0x29, 0x2a, 0x35, 0x36, 0x37, 0x38,
			0x39, 0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
			0x49, 0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58,
			0x59, 0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68,
			0x69, 0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78,
			0x79, 0x7a, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87,
			0x88, 0x89, 0x8a, 0x92, 0x93, 0x94, 0x95, 0x96,
			0x97, 0x98, 0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5,
			0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4,
			0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3,
			0xc4, 0xc5, 0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2,
			0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda,
			0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9,
			0xea, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
			0xf9, 0xfa,
		},
	},
}

// huffmanLUT maps a value to (codeword size << 24 | codeword).
type huffmanLUT []uint32

func (h *huffmanLUT) init(s huffmanSpec) {
	maxValue := 0
	for _, v := range s.value {
		maxValue = max(maxValue, int(v))
	}
	*h = make([]uint32, maxValue+1)
	code, k := uint32(0), 0
	for i := 0; i < len(s.count); i++ {
		nBits := uint32(i+1) << 24
		for j := uint8(0); j < s.count[i]; j++ {
			(*h)[s.value[k]] = nBits | code
			code++
			k++
		}
		code <<= 1
	}
}

var theHuffmanLUT [4]huffmanLUT

func init() {
	for i, s := range theHuffmanSpec {
		theHuffmanLUT[i].init(s)
	}
}

const (
	sof0Marker = 0xc0
	dhtMarker  = 0xc4
	rst0Marker = 0xd0
	soiMarker  = 0xd8
	eoiMarker  = 0xd9
	dqtMarker  = 0xdb
	driMarker  = 0xdd
)

// strip entropy-codes one restart interval into its own buffer.
type strip struct {
	out         []byte
	bits, nBits uint32
	quant       *[2][blockSize]byte
}

func (s *strip) emit(bits, nBits uint32) {
	nBits += s.nBits
	bits <<= 32 - nBits
	bits |= s.bits
	for nBits >= 8 {
		b := uint8(bits >> 24)
		s.out = append(s.out, b)
		if b == 0xff {
			s.out = append(s.out, 0x00)
		}
		bits <<= 8
		nBits -= 8
	}
	s.bits, s.nBits = bits, nBits
}

func (s *strip) emitHuff(h int, value int32) {
	x := theHuffmanLUT[h][value]
	s.emit(x&(1<<24-1), x>>24)
}

func (s *strip) emitHuffRLE(h int, runLength, value int32) {
	a, b := value, value
	if a < 0 {
		a, b = -value, value-1
	}
	var nBits uint32
	if a < 0x100 {
		nBits = uint32(bitCount[a])
	} else {
		nBits = 8 + uint32(bitCount[a>>8])
	}
	s.emitHuff(h, runLength<<4|int32(nBits))
	if nBits > 0 {
		s.emit(uint32(b)&(1<<nBits-1), nBits)
	}
}

func (s *strip) writeBlock(b *block, q int, prevDC int32) int32 {
	fdct(b)
	dc := div(b[0], 8*int32(s.quant[q][0]))
	s.emitHuffRLE(2*q, 0, dc-prevDC)
	h, runLength := 2*q+1, int32(0)
	for zig := 1; zig < blockSize; zig++ {
		ac := div(b[unzig[zig]], 8*int32(s.quant[q][zig]))
		if ac == 0 {
			runLength++
		} else {
			for runLength > 15 {
				s.emitHuff(h, 0xf0)
				runLength -= 16
			}
			s.emitHuffRLE(h, runLength, ac)
			runLength = 0
		}
	}
	if runLength > 0 {
		s.emitHuff(h, 0x00)
	}
	return dc
}

// rgbaToYCbCr converts the 8x8 region of m at p, replicating edge pixels.
func rgbaToYCbCr(m *image.RGBA, p image.Point, yBlock, cbBlock, crBlock *block) {
	b := m.Bounds()
	xmax := b.Max.X - 1
	ymax := b.Max.Y - 1
	for j := 0; j < 8; j++ {
		sj := min(p.Y+j, ymax)
		offset := (sj-b.Min.Y)*m.Stride - b.Min.X*4
		for i := 0; i < 8; i++ {
			sx := min(p.X+i, xmax)
			pix := m.Pix[offset+sx*4 : offset+sx*4+3 : offset+sx*4+3]
			r, g, bb := int32(pix[0]), int32(pix[1]), int32(pix[2])
			// Same fixed-point formulas as color.RGBToYCbCr.
			yy := (19595*r + 38470*g + 7471*bb + 1<<15) >> 16
			cb := -11056*r - 21712*g + 32768*bb + 257<<15
			if uint32(cb)&0xff000000 == 0 {
				cb >>= 16
			} else {
				cb = ^(cb >> 31) & 0xff
			}
			cr := 32768*r - 27440*g - 5328*bb + 257<<15
			if uint32(cr)&0xff000000 == 0 {
				cr >>= 16
			} else {
				cr = ^(cr >> 31) & 0xff
			}
			yBlock[8*j+i] = yy
			cbBlock[8*j+i] = cb
			crBlock[8*j+i] = cr
		}
	}
}

func scale(dst *block, src *[4]block) {
	for i := 0; i < 4; i++ {
		dstOff := (i&2)<<4 | (i&1)<<2
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				j := 16*y + 2*x
				sum := src[i][j] + src[i][j+1] + src[i][j+8] + src[i][j+9]
				dst[8*y+x+dstOff] = (sum + 2) >> 2
			}
		}
	}
}

// encode codes MCU rows [row0, row1) (16 pixel rows each).
func (s *strip) encode(m *image.RGBA, row0, row1 int) {
	var (
		b                           block
		cb, cr                      [4]block
		prevDCY, prevDCCb, prevDCCr int32
	)
	bounds := m.Bounds()
	for y := bounds.Min.Y + row0*16; y < bounds.Min.Y+row1*16 && y < bounds.Max.Y; y += 16 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 16 {
			for i := 0; i < 4; i++ {
				p := image.Pt(x+(i&1)*8, y+(i&2)*4)
				rgbaToYCbCr(m, p, &b, &cb[i], &cr[i])
				prevDCY = s.writeBlock(&b, 0, prevDCY)
			}
			scale(&b, &cb)
			prevDCCb = s.writeBlock(&b, 1, prevDCCb)
			scale(&b, &cr)
			prevDCCr = s.writeBlock(&b, 1, prevDCCr)
		}
	}
	s.emit(0x7f, 7) // pad the last byte with 1's
}

// Options are the encoding parameters.
type Options struct {
	// Quality ranges from 1 to 100; 0 means 75.
	Quality int
	// Workers bounds the parallel strips; 0 means GOMAXPROCS.
	Workers int
}

var sosHeader = []byte{0xff, 0xda, 0x00, 0x0c, 0x03, 0x01, 0x00, 0x02, 0x11, 0x03, 0x11, 0x00, 0x3f, 0x00}

// Encode writes m to w as a 4:2:0 baseline JPEG.
func Encode(w io.Writer, m *image.RGBA, o *Options) error {
	b := m.Bounds()
	if b.Dx() >= 1<<16 || b.Dy() >= 1<<16 {
		return errors.New("jpeg: image is too large to encode")
	}
	if b.Empty() {
		return errors.New("jpeg: image is empty")
	}
	quality, workers := 75, runtime.GOMAXPROCS(0)
	if o != nil {
		if o.Quality != 0 {
			quality = min(max(o.Quality, 1), 100)
		}
		if o.Workers > 0 {
			workers = o.Workers
		}
	}
	scaleFactor := 200 - quality*2
	if quality < 50 {
		scaleFactor = 5000 / quality
	}
	var quant [2][blockSize]byte
	for i := range quant {
		for j := range quant[i] {
			x := (int(unscaledQuant[i][j])*scaleFactor + 50) / 100
			quant[i][j] = uint8(min(max(x, 1), 255))
		}
	}

	mcuCols := (b.Dx() + 15) / 16
	mcuRows := (b.Dy() + 15) / 16
	// A restart interval must fit in 16 bits of MCUs; strips are whole MCU
	// rows so every interval starts at the left edge.
	rowsPerStrip := max((mcuRows+workers*2-1)/(workers*2), 1)
	for rowsPerStrip*mcuCols > 0xffff {
		rowsPerStrip--
	}
	if rowsPerStrip < 1 {
		return errors.New("jpeg: image is too wide for restart intervals")
	}
	nStrips := (mcuRows + rowsPerStrip - 1) / rowsPerStrip
	strips := make([]strip, nStrips)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range strips {
		strips[i] = strip{quant: &quant, out: make([]byte, 0, 64<<10)}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			strips[i].encode(m, i*rowsPerStrip, min((i+1)*rowsPerStrip, mcuRows))
		}(i)
	}

	hdr := make([]byte, 0, 1024)
	hdr = append(hdr, 0xff, soiMarker)
	// DQT
	hdr = append(hdr, 0xff, dqtMarker, 0, 2+2*(1+blockSize))
	for i := range quant {
		hdr = append(hdr, byte(i))
		hdr = append(hdr, quant[i][:]...)
	}
	// SOF0, 4:2:0
	hdr = append(hdr, 0xff, sof0Marker, 0, 17, 8, byte(b.Dy()>>8), byte(b.Dy()), byte(b.Dx()>>8), byte(b.Dx()), 3,
		1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1)
	// DHT
	dhtLen := 2
	for _, s := range theHuffmanSpec {
		dhtLen += 1 + 16 + len(s.value)
	}
	hdr = append(hdr, 0xff, dhtMarker, byte(dhtLen>>8), byte(dhtLen))
	for i, s := range theHuffmanSpec {
		hdr = append(hdr, "\x00\x10\x01\x11"[i])
		hdr = append(hdr, s.count[:]...)
		hdr = append(hdr, s.value...)
	}
	// DRI
	interval := rowsPerStrip * mcuCols
	hdr = append(hdr, 0xff, driMarker, 0, 4, byte(interval>>8), byte(interval))
	hdr = append(hdr, sosHeader...)

	wg.Wait()
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	for i := range strips {
		if _, err := w.Write(strips[i].out); err != nil {
			return err
		}
		if i < len(strips)-1 {
			if _, err := w.Write([]byte{0xff, byte(rst0Marker + i%8)}); err != nil {
				return err
			}
		}
	}
	_, err := w.Write([]byte{0xff, eoiMarker})
	return err
}
