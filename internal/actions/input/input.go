// Package input implements /v1/input: mouse, keyboard and batches. All input
// is serialised inside the service (spec section 5.7).
package input

// Button is a mouse button.
type Button string

const (
	ButtonLeft   Button = "left"
	ButtonRight  Button = "right"
	ButtonMiddle Button = "middle"
)

// Backend injects input events. Coordinates are virtual-desktop physical
// pixels; scroll amounts are wheel ticks, positive up or right.
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
}

// Key is one entry of the key table in docs/keys.md.
type Key struct {
	Name   string
	VK     uint16 // Windows virtual-key code
	Scan   uint16 // Windows set-1 scan code
	Ext    bool   // Windows extended-key flag
	Keysym uint32 // X11 keysym
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }
