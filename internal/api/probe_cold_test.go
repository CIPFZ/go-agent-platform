package api

import (
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// coldScreen behaves like the Wayland backend on a cold start: the first
// enumeration takes a portal screenshot that is slower than the old 2 s
// enumeration budget; later ones are cached.
type coldScreen struct {
	fakeScreen
	once    sync.Once
	ready   chan struct{}
	delay   time.Duration
	calls   atomic.Int64
	failAll bool
}

func (c *coldScreen) Displays() ([]screen.Display, error) {
	c.calls.Add(1)
	if c.failAll {
		return nil, errors.New("portal unavailable")
	}
	c.once.Do(func() {
		time.Sleep(c.delay)
		close(c.ready)
	})
	<-c.ready
	return []screen.Display{{Index: 0, Bounds: protocol.Rect{W: 1920, H: 1080}, Scale: 1, Primary: true}}, nil
}

func (c *coldScreen) Capture(r protocol.Rect) (*image.RGBA, error) {
	return image.NewRGBA(image.Rect(0, 0, r.W, r.H)), nil
}

// The first request after startup is probe: it must report the real
// display, not zero displays.
func TestColdWaylandFirstRequestIsProbe(t *testing.T) {
	cs := &coldScreen{ready: make(chan struct{}), delay: 2500 * time.Millisecond}
	e := newEnv(t, func(d *Deps) {
		d.Screen = cs
		d.Wayland = true
	})
	var p protocol.ProbeResult
	e.call("GET", "/v1/probe", nil).into(t, &p)
	if !p.Display.Ready || p.Display.Count != 1 || p.Display.Primary == nil || p.Display.Primary.W != 1920 {
		t.Fatalf("cold probe display = %+v", p.Display)
	}
	if cs.calls.Load() < 2 {
		t.Fatalf("no startup warm-up: %d enumerations", cs.calls.Load())
	}
}

// When the display cannot be enumerated at all, probe says it is not ready
// instead of ready with zero displays.
func TestProbeNeverReadyWithZeroDisplays(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Screen = &coldScreen{failAll: true} })
	var p protocol.ProbeResult
	e.call("GET", "/v1/probe", nil).into(t, &p)
	if p.Display.Ready || p.Display.Count != 0 {
		t.Fatalf("display = %+v", p.Display)
	}
	var caps protocol.CapabilitiesResult
	e.call("GET", "/v1/capabilities", nil).into(t, &caps)
	for _, a := range caps.Actions {
		if a == "screen" || a == "input" {
			t.Fatalf("display actions advertised for an unusable display: %v", caps.Actions)
		}
	}
}
