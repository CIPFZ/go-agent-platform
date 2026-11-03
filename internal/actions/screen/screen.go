// Package screen implements /v1/screen: display enumeration, capture and the
// cursor. Coordinates are physical pixels of the virtual desktop with the
// primary display's top-left corner as origin (spec D1, section 6.2).
package screen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/imaging/jpeg"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Display is one monitor. Index 0 is the primary display.
type Display struct {
	Index   int           `json:"index"`
	Bounds  protocol.Rect `json:"bounds"`
	Scale   float64       `json:"scale"`
	Primary bool          `json:"primary"`
}

// Cursor is the pointer position and visibility.
type Cursor struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Visible bool `json:"visible"`
}

// CursorImage is the current pointer shape for overlaying on captures.
type CursorImage struct {
	Image   *image.RGBA
	Hotspot image.Point
}

// Backend is the platform capture implementation. Calls may block; the
// Service runs them on their own goroutine under a timeout.
type Backend interface {
	// Displays returns the monitors, primary first, in API coordinates.
	Displays() ([]Display, error)
	// Capture grabs r (API coordinates, within the virtual desktop).
	Capture(r protocol.Rect) (*image.RGBA, error)
	Cursor() (Cursor, error)
	CursorImage() (*CursorImage, error)
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }

// Options are the screen/capture request parameters (spec section 6.2).
type Options struct {
	Display int
	Region  *protocol.Rect
	Cursor  bool
	Format  string // "jpeg" or "png"
	Quality int    // JPEG quality 1..100
}

// Shot is an encoded capture.
type Shot struct {
	Data       []byte
	Format     string
	Width      int
	Height     int
	Cursor     protocol.Point
	CapturedAt time.Time
	SHA256     string
}

// DegradedAfter is the number of consecutive capture timeouts after which
// the display is reported as degraded (spec section 7).
const DegradedAfter = 3

// Service wraps a Backend with the protocol semantics of /v1/screen.
type Service struct {
	backend Backend
	now     func() time.Time

	mu       sync.Mutex
	timeouts int
}

// NewService returns a Service over b.
func NewService(b Backend) *Service { return &Service{backend: b, now: time.Now} }

// Degraded reports whether the last DegradedAfter captures all timed out.
func (s *Service) Degraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timeouts >= DegradedAfter
}

func (s *Service) recordOutcome(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errors.Is(err, context.DeadlineExceeded) {
		s.timeouts++
	} else if err == nil {
		s.timeouts = 0
	}
}

// Displays lists monitors under ctx.
func (s *Service) Displays(ctx context.Context) ([]Display, error) {
	return async.Run(ctx, s.backend.Displays)
}

// Cursor returns the pointer state under ctx.
func (s *Service) Cursor(ctx context.Context) (Cursor, error) {
	return async.Run(ctx, s.backend.Cursor)
}

// Validate checks capture options and fills defaults.
func (o *Options) Validate() error {
	switch o.Format {
	case "":
		o.Format = "jpeg"
	case "jpeg", "png":
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "format must be jpeg or png")
	}
	if o.Quality == 0 {
		o.Quality = 85
	}
	if o.Quality < 1 || o.Quality > 100 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "quality must be between 1 and 100")
	}
	if o.Display < -1 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "display must be -1 or a display index")
	}
	if o.Region != nil && o.Region.Empty() {
		return protocol.Errorf(protocol.CodeInvalidRequest, "region width and height must be positive")
	}
	return nil
}

// TargetRect resolves the display and region of o against displays: the
// display bounds (or the bounding box of all displays for -1), intersected
// with the region when one is given.
func TargetRect(displays []Display, o Options) (protocol.Rect, error) {
	if len(displays) == 0 {
		return protocol.Rect{}, protocol.Errorf(protocol.CodeNoDisplay, "no display is available")
	}
	var bounds protocol.Rect
	if o.Display == -1 {
		for _, d := range displays {
			bounds = bounds.Union(d.Bounds)
		}
	} else {
		if o.Display >= len(displays) {
			return protocol.Rect{}, protocol.Errorf(protocol.CodeInvalidRequest, "display %d does not exist; %d displays are attached", o.Display, len(displays))
		}
		bounds = displays[o.Display].Bounds
	}
	if o.Region == nil {
		return bounds, nil
	}
	clipped := o.Region.Intersect(bounds)
	if clipped.Empty() {
		return protocol.Rect{}, protocol.Errorf(protocol.CodeInvalidRequest, "region lies entirely outside the display").WithReason(protocol.ReasonRegionOutOfBounds)
	}
	return clipped, nil
}

// Capture takes and encodes a screenshot. Grabbing and overlaying run on a
// separate goroutine that is abandoned when ctx ends (spec section 7).
func (s *Service) Capture(ctx context.Context, o Options) (*Shot, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type grabbed struct {
		img    *image.RGBA
		rect   protocol.Rect
		cursor Cursor
		at     time.Time
	}
	g, err := async.Run(ctx, func() (grabbed, error) {
		displays, err := s.backend.Displays()
		if err != nil {
			return grabbed{}, err
		}
		rect, err := TargetRect(displays, o)
		if err != nil {
			return grabbed{}, err
		}
		at := s.now()
		img, err := s.backend.Capture(rect)
		if err != nil {
			return grabbed{}, err
		}
		cursor, cerr := s.backend.Cursor()
		if o.Cursor && cerr == nil && cursor.Visible {
			s.overlayCursor(img, rect, cursor)
		}
		return grabbed{img: img, rect: rect, cursor: cursor, at: at}, nil
	})
	s.recordOutcome(err)
	if err != nil {
		return nil, err
	}
	data, err := Encode(g.img, o.Format, o.Quality)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &Shot{
		Data:       data,
		Format:     o.Format,
		Width:      g.img.Bounds().Dx(),
		Height:     g.img.Bounds().Dy(),
		Cursor:     protocol.Point{X: g.cursor.X, Y: g.cursor.Y},
		CapturedAt: g.at,
		SHA256:     hex.EncodeToString(sum[:]),
	}, nil
}

func (s *Service) overlayCursor(img *image.RGBA, rect protocol.Rect, c Cursor) {
	ci, err := s.backend.CursorImage()
	if err != nil || ci == nil || ci.Image == nil {
		ci = DefaultArrow()
	}
	at := image.Pt(c.X-ci.Hotspot.X-rect.X, c.Y-ci.Hotspot.Y-rect.Y)
	dst := image.Rectangle{Min: at, Max: at.Add(ci.Image.Bounds().Size())}
	draw.Draw(img, dst, ci.Image, ci.Image.Bounds().Min, draw.Over)
}

// Encode encodes img as "jpeg" or "png" (PNG at the fastest compression
// level, spec section 6.2).
func Encode(img *image.RGBA, format string, quality int) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(img.Bounds().Dx() * img.Bounds().Dy() / 4)
	var err error
	if format == "png" {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	}
	return buf.Bytes(), err
}
