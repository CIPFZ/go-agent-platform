package jpeg

import (
	"bytes"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"math/rand"
	"testing"
)

func testImage(w, h int, seed int64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{uint8(x * 7), uint8(y * 3), uint8(r.Intn(256)), 255}
			if (x/5+y/3)%4 == 0 {
				c = color.RGBA{255, 255, 255, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// Decoding our parallel restart-interval stream must give exactly the pixels
// of the standard library's single-scan stream: DCT and quantisation are
// identical, only the entropy-coded segmentation differs.
func TestMatchesStandardLibraryPixels(t *testing.T) {
	sizes := [][2]int{{1, 1}, {7, 5}, {16, 16}, {17, 33}, {640, 480}, {1366, 768}, {1920, 1080}}
	for _, size := range sizes {
		for _, workers := range []int{1, 3, 8} {
			for _, quality := range []int{1, 50, 85, 100} {
				img := testImage(size[0], size[1], int64(size[0]*size[1]))
				var ours, std bytes.Buffer
				if err := Encode(&ours, img, &Options{Quality: quality, Workers: workers}); err != nil {
					t.Fatalf("%v: %v", size, err)
				}
				if err := stdjpeg.Encode(&std, img, &stdjpeg.Options{Quality: quality}); err != nil {
					t.Fatal(err)
				}
				a, err := stdjpeg.Decode(bytes.NewReader(ours.Bytes()))
				if err != nil {
					t.Fatalf("%v workers=%d q=%d: decode ours: %v", size, workers, quality, err)
				}
				b, _ := stdjpeg.Decode(bytes.NewReader(std.Bytes()))
				ya, yb := a.(*image.YCbCr), b.(*image.YCbCr)
				if !a.Bounds().Eq(img.Bounds()) || !bytes.Equal(ya.Y, yb.Y) || !bytes.Equal(ya.Cb, yb.Cb) || !bytes.Equal(ya.Cr, yb.Cr) {
					t.Fatalf("%v workers=%d q=%d: decoded pixels differ from image/jpeg", size, workers, quality)
				}
			}
		}
	}
}

func TestVeryWideImageShrinksRestartInterval(t *testing.T) {
	img := testImage(65535, 40, 1)
	var buf bytes.Buffer
	if err := Encode(&buf, img, &Options{Quality: 50, Workers: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := stdjpeg.Decode(&buf); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestRejectsEmptyAndOversized(t *testing.T) {
	if Encode(&bytes.Buffer{}, image.NewRGBA(image.Rect(0, 0, 0, 5)), nil) == nil {
		t.Error("empty image accepted")
	}
	if Encode(&bytes.Buffer{}, &image.RGBA{Rect: image.Rect(0, 0, 1<<16, 1)}, nil) == nil {
		t.Error("oversized image accepted")
	}
}

func BenchmarkEncode1080p(b *testing.B) {
	img := testImage(1920, 1080, 1)
	b.Run("parallel", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = Encode(&bytes.Buffer{}, img, &Options{Quality: 85})
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = stdjpeg.Encode(&bytes.Buffer{}, img, &stdjpeg.Options{Quality: 85})
		}
	})
}
