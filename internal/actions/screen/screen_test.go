package screen

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"image/png"
	"sync"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

type fakeBackend struct {
	displays  []Display
	cursor    Cursor
	cursorImg *CursorImage
	cursorErr error
	block     chan struct{}
	mu        sync.Mutex
	captured  []protocol.Rect
}

func (f *fakeBackend) Displays() ([]Display, error) { return f.displays, nil }

func (f *fakeBackend) Capture(r protocol.Rect) (*image.RGBA, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.captured = append(f.captured, r)
	f.mu.Unlock()
	img := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img, nil
}

func (f *fakeBackend) Cursor() (Cursor, error) { return f.cursor, nil }

func (f *fakeBackend) CursorImage() (*CursorImage, error) { return f.cursorImg, f.cursorErr }

func twoDisplays() []Display {
	return []Display{
		{Index: 0, Bounds: protocol.Rect{X: 0, Y: 0, W: 1920, H: 1080}, Scale: 1.25, Primary: true},
		{Index: 1, Bounds: protocol.Rect{X: -1280, Y: 0, W: 1280, H: 1024}, Scale: 1},
	}
}

func TestTargetRect(t *testing.T) {
	d := twoDisplays()
	cases := []struct {
		name string
		opts Options
		want protocol.Rect
		err  protocol.Code
	}{
		{"primary by default", Options{}, protocol.Rect{W: 1920, H: 1080}, ""},
		{"secondary at negative x", Options{Display: 1}, protocol.Rect{X: -1280, W: 1280, H: 1024}, ""},
		{"virtual desktop", Options{Display: -1}, protocol.Rect{X: -1280, W: 3200, H: 1080}, ""},
		{"region clipped to display", Options{Region: &protocol.Rect{X: 1900, Y: 1000, W: 100, H: 100}}, protocol.Rect{X: 1900, Y: 1000, W: 20, H: 80}, ""},
		{"region across both on virtual desktop", Options{Display: -1, Region: &protocol.Rect{X: -10, Y: 5, W: 20, H: 5}}, protocol.Rect{X: -10, Y: 5, W: 20, H: 5}, ""},
		{"region fully outside", Options{Region: &protocol.Rect{X: -500, Y: 0, W: 10, H: 10}}, protocol.Rect{}, protocol.CodeInvalidRequest},
		{"display out of range", Options{Display: 2}, protocol.Rect{}, protocol.CodeInvalidRequest},
	}
	for _, tc := range cases {
		got, err := TargetRect(d, tc.opts)
		if tc.err != "" {
			var perr *protocol.Error
			if !errors.As(err, &perr) || perr.Code != tc.err {
				t.Errorf("%s: err = %v, want %s", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
}

func TestValidateDefaults(t *testing.T) {
	o := Options{}
	if err := o.Validate(); err != nil || o.Format != "jpeg" || o.Quality != 85 {
		t.Fatalf("defaults = %+v, %v", o, err)
	}
	for _, bad := range []Options{{Format: "gif"}, {Quality: 101}, {Quality: -1}, {Display: -2}, {Region: &protocol.Rect{W: 0, H: 5}}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted", bad)
		}
	}
}

func TestCaptureEncodesAndReportsMetadata(t *testing.T) {
	f := &fakeBackend{displays: twoDisplays(), cursor: Cursor{X: 10, Y: 20, Visible: true}}
	s := NewService(f)
	for _, format := range []string{"jpeg", "png"} {
		shot, err := s.Capture(context.Background(), Options{Format: format, Region: &protocol.Rect{X: 0, Y: 0, W: 64, H: 32}})
		if err != nil {
			t.Fatal(err)
		}
		var img image.Image
		if format == "png" {
			img, err = png.Decode(bytes.NewReader(shot.Data))
		} else {
			img, err = stdjpeg.Decode(bytes.NewReader(shot.Data))
		}
		if err != nil || img.Bounds().Dx() != 64 || img.Bounds().Dy() != 32 {
			t.Fatalf("%s: decode %v bounds %v", format, err, img)
		}
		if shot.Width != 64 || shot.Height != 32 || shot.Cursor != (protocol.Point{X: 10, Y: 20}) || len(shot.SHA256) != 64 || shot.CapturedAt.IsZero() {
			t.Fatalf("%s: shot = %+v", format, shot)
		}
	}
}

func TestCursorOverlayFallsBackToArrow(t *testing.T) {
	f := &fakeBackend{displays: twoDisplays(), cursor: Cursor{X: 2, Y: 2, Visible: true}, cursorErr: errors.New("no shape")}
	s := NewService(f)
	shot, err := s.Capture(context.Background(), Options{Format: "png", Cursor: true, Region: &protocol.Rect{W: 40, H: 40}})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(shot.Data))
	// The arrow's outline pixel sits at the hotspot (2,2).
	if r, g, b, _ := img.At(2, 2).RGBA(); r != 0 || g != 0 || b != 0 {
		t.Fatalf("cursor not drawn at hotspot: %v", img.At(2, 2))
	}
	if r, _, _, _ := img.At(30, 30).RGBA(); r != 0xffff {
		t.Fatal("overlay painted outside the cursor")
	}
}

func TestCursorOverlayUsesShapeAndHotspot(t *testing.T) {
	shape := image.NewRGBA(image.Rect(0, 0, 3, 3))
	shape.SetRGBA(1, 1, color.RGBA{0, 0, 255, 255})
	f := &fakeBackend{displays: twoDisplays(), cursor: Cursor{X: 11, Y: 11, Visible: true}, cursorImg: &CursorImage{Image: shape, Hotspot: image.Pt(1, 1)}}
	shot, err := NewService(f).Capture(context.Background(), Options{Format: "png", Cursor: true, Region: &protocol.Rect{X: 10, Y: 10, W: 5, H: 5}})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(shot.Data))
	// Cursor (11,11) minus region origin (10,10) puts the hotspot at (1,1).
	if r, g, b, _ := img.At(1, 1).RGBA(); r != 0 || g != 0 || b != 0xffff {
		t.Fatalf("hotspot pixel = %v", img.At(1, 1))
	}
}

func TestTimeoutsMarkDegradedAndRecover(t *testing.T) {
	f := &fakeBackend{displays: twoDisplays(), block: make(chan struct{})}
	s := NewService(f)
	for i := 0; i < DegradedAfter; i++ {
		if s.Degraded() {
			t.Fatalf("degraded after %d timeouts", i)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := s.Capture(ctx, Options{})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	}
	if !s.Degraded() {
		t.Fatal("not degraded after consecutive timeouts")
	}
	close(f.block)
	if _, err := s.Capture(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if s.Degraded() {
		t.Fatal("still degraded after a successful capture")
	}
}
