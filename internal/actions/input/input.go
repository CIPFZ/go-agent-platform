// Package input implements /v1/input: mouse, keyboard and batches. All input
// is serialised inside the service (spec section 5.7): a request waits in the
// queue under its own deadline and is never executed once that deadline has
// passed.
package input

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Button is a mouse button.
type Button string

const (
	ButtonLeft   Button = "left"
	ButtonRight  Button = "right"
	ButtonMiddle Button = "middle"
)

// Backend injects input events. Coordinates are virtual-desktop physical
// pixels; scroll amounts are wheel ticks, positive up or right. Backends may
// buffer events; Flush makes sure everything sent so far has been delivered.
type Backend interface {
	Move(x, y int) error
	Position() (x, y int, err error)
	Button(b Button, down bool) error
	Scroll(dx, dy int) error
	Key(k Key, down bool) error
	// TypeUnicode types one character as text (KEYEVENTF_UNICODE, keysym).
	TypeUnicode(r rune) error
	// TypeScancode types one ASCII character through physical key codes.
	TypeScancode(r rune) error
	// Begin and End bracket one request so backends can prepare and restore
	// state such as temporary keyboard mappings.
	Begin() error
	End()
}

// Key is one entry of the key table in docs/keys.md.
type Key struct {
	Name   string
	VK     uint16 // Windows virtual-key code
	Scan   uint16 // Windows set-1 scan code
	Ext    bool   // Windows extended-key flag
	Keysym uint32 // X11 keysym
	Evdev  uint16 // Linux evdev key code (Wayland input through uinput)
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }

// Limits of spec sections 6.3 and 7.
const (
	MaxClickCount   = 5
	MaxBatchActions = 200
	BaseTimeout     = 5 * time.Second
	BatchTimeoutCap = 60 * time.Second
	minStepInterval = 8 * time.Millisecond
	minStepPixels   = 2
	DefaultDragMS   = 300
	DefaultTypeMS   = 5
	// clickHold is how long a click keeps the button down. Window managers
	// that grab buttons for click-to-focus and replay them (openbox, mutter)
	// can drop a press and release that arrive in the same instant.
	clickHold = 10 * time.Millisecond
)

// Service serialises input through one backend.
type Service struct {
	backend Backend
	queue   chan struct{}
	sleep   func(ctx context.Context, d time.Duration) error
	hold    func(d time.Duration)
}

// NewService returns a Service over b.
func NewService(b Backend) *Service {
	return &Service{backend: b, queue: make(chan struct{}, 1), sleep: sleepCtx, hold: time.Sleep}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run executes fn while holding the input queue. Waiting for the queue is
// bounded by ctx; a request whose deadline passes while queued never runs.
func (s *Service) Run(ctx context.Context, fn func(ctx context.Context, x *Exec) error) error {
	select {
	case s.queue <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.queue }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.backend.Begin(); err != nil {
		return err
	}
	defer s.backend.End()
	x := &Exec{s: s}
	err := fn(ctx, x)
	x.releaseHeld()
	return err
}

// Exec executes actions inside Run. It tracks buttons and keys that an
// action pressed on its own behalf, so an action interrupted half way (a
// drag, a hotkey) does not leave them stuck down.
type Exec struct {
	s           *Service
	heldButtons []Button
	heldKeys    []Key
}

func (x *Exec) b() Backend { return x.s.backend }

func (x *Exec) releaseHeld() {
	for i := len(x.heldKeys) - 1; i >= 0; i-- {
		_ = x.b().Key(x.heldKeys[i], false)
	}
	for _, btn := range x.heldButtons {
		_ = x.b().Button(btn, false)
	}
	x.heldKeys, x.heldButtons = nil, nil
}

// Point is a request coordinate.
type Point = protocol.Point

// ParseButton validates a button name; empty means left.
func ParseButton(name string) (Button, error) {
	switch Button(name) {
	case "", ButtonLeft:
		return ButtonLeft, nil
	case ButtonRight, ButtonMiddle:
		return Button(name), nil
	}
	return "", protocol.Errorf(protocol.CodeInvalidRequest, "button must be left, right or middle")
}

// ResolveKey looks up a key name, reporting unknown names as invalid_request.
func ResolveKey(name string) (Key, error) {
	k, ok := LookupKey(name)
	if !ok {
		return Key{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown key %q; see docs/keys.md", name).WithReason(protocol.ReasonUnknownKey)
	}
	return k, nil
}

// MoveTo moves the pointer to (x, y). With a positive duration the path is
// linearly interpolated in steps of at least 8 ms and 2 pixels (spec 6.3).
func (x *Exec) MoveTo(ctx context.Context, to Point, duration time.Duration) error {
	if duration <= 0 {
		return x.b().Move(to.X, to.Y)
	}
	fx, fy, err := x.b().Position()
	if err != nil {
		return err
	}
	return x.glide(ctx, Point{X: fx, Y: fy}, to, duration)
}

func (x *Exec) glide(ctx context.Context, from, to Point, duration time.Duration) error {
	dist := math.Hypot(float64(to.X-from.X), float64(to.Y-from.Y))
	steps := int(min(float64(duration/minStepInterval), dist/minStepPixels))
	if steps < 1 {
		if err := x.s.sleep(ctx, duration); err != nil {
			return err
		}
		return x.b().Move(to.X, to.Y)
	}
	interval := duration / time.Duration(steps)
	start := time.Now()
	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		px := from.X + int(math.Round(float64(to.X-from.X)*f))
		py := from.Y + int(math.Round(float64(to.Y-from.Y)*f))
		if err := x.b().Move(px, py); err != nil {
			return err
		}
		if i < steps {
			// Sleep to the scheduled time of the next step so slow event
			// delivery does not stretch the whole movement.
			if err := x.s.sleep(ctx, time.Until(start.Add(interval*time.Duration(i)))); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// Click clicks count times at the current position, or at at when given.
func (x *Exec) Click(ctx context.Context, at *Point, btn Button, count int) error {
	if at != nil {
		if err := x.b().Move(at.X, at.Y); err != nil {
			return err
		}
	}
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := x.pressButton(btn); err != nil {
			return err
		}
		// Not cancellable: once the press is out the click completes, so a
		// batch interrupted here still counts it as executed.
		x.s.hold(clickHold)
		if err := x.releaseButton(btn); err != nil {
			return err
		}
	}
	return nil
}

func (x *Exec) pressButton(btn Button) error {
	if err := x.b().Button(btn, true); err != nil {
		return err
	}
	x.heldButtons = append(x.heldButtons, btn)
	return nil
}

func (x *Exec) releaseButton(btn Button) error {
	for i, held := range x.heldButtons {
		if held == btn {
			x.heldButtons = append(x.heldButtons[:i], x.heldButtons[i+1:]...)
			break
		}
	}
	return x.b().Button(btn, false)
}

// ButtonState presses or releases btn and leaves it that way (mouse/down|up).
func (x *Exec) ButtonState(btn Button, down bool) error { return x.b().Button(btn, down) }

// Drag presses btn at from, glides to to over duration and releases.
func (x *Exec) Drag(ctx context.Context, from, to Point, btn Button, duration time.Duration) error {
	if err := x.b().Move(from.X, from.Y); err != nil {
		return err
	}
	if err := x.pressButton(btn); err != nil {
		return err
	}
	if err := x.glide(ctx, from, to, duration); err != nil {
		return err
	}
	return x.releaseButton(btn)
}

// Scroll scrolls by wheel ticks.
func (x *Exec) Scroll(dx, dy int) error { return x.b().Scroll(dx, dy) }

// Type types text one character at a time with interval between characters.
// "\n" presses Enter and "\t" presses Tab; "\r\n" counts as one Enter.
func (x *Exec) Type(ctx context.Context, text string, interval time.Duration, scancode bool) error {
	if scancode {
		for _, r := range text {
			if r > 0x7e || (r < 0x20 && r != '\n' && r != '\t' && r != '\r') {
				return protocol.Errorf(protocol.CodeInvalidRequest, "method scancode only supports printable ASCII, tab and newline")
			}
		}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	first := true
	for _, r := range text {
		if !first {
			if err := x.s.sleep(ctx, interval); err != nil {
				return err
			}
		}
		first = false
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		switch {
		case r == '\n' || r == '\r':
			err = x.tap(mustKey("enter"))
		case r == '\t':
			err = x.tap(mustKey("tab"))
		case scancode:
			err = x.b().TypeScancode(r)
		default:
			err = x.b().TypeUnicode(r)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (x *Exec) tap(k Key) error {
	if err := x.b().Key(k, true); err != nil {
		return err
	}
	if err := x.b().Key(k, false); err != nil {
		x.heldKeys = append(x.heldKeys, k) // retried by releaseHeld
		return err
	}
	return nil
}

// Press taps one key.
func (x *Exec) Press(k Key) error { return x.tap(k) }

// KeyState presses or releases k and leaves it that way (key/down|up).
func (x *Exec) KeyState(k Key, down bool) error { return x.b().Key(k, down) }

// Hotkey presses keys in order and releases them in reverse order.
func (x *Exec) Hotkey(ctx context.Context, keys []Key) error {
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := x.b().Key(k, true); err != nil {
			return err
		}
		x.heldKeys = append(x.heldKeys, k)
	}
	for len(x.heldKeys) > 0 {
		k := x.heldKeys[len(x.heldKeys)-1]
		x.heldKeys = x.heldKeys[:len(x.heldKeys)-1]
		if err := x.b().Key(k, false); err != nil {
			return err
		}
	}
	return nil
}

// Wait sleeps for d (batch "wait").
func (x *Exec) Wait(ctx context.Context, d time.Duration) error { return x.s.sleep(ctx, d) }

func mustKey(name string) Key {
	k, ok := LookupKey(name)
	if !ok {
		panic("missing key " + name)
	}
	return k
}

// TypeDuration is the expected duration of typing text at interval, used to
// extend the default timeout of key/type.
func TypeDuration(text string, interval time.Duration) time.Duration {
	n := utf8.RuneCountInString(text)
	if n <= 1 {
		return 0
	}
	return time.Duration(n-1) * interval
}
