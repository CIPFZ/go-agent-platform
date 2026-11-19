package remote

import (
	"context"
	"encoding/json"

	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
)

// SecureDesktop reaches the desktop that vm-agent itself cannot: the lock
// screen and other secure desktops belong to the system, and only a SYSTEM
// process attached to them can capture them or send them input. The helper
// behind this interface is such a process (docs/remote-desktop-design.md
// 5.14); without it a locked desktop can only be waited out.
type SecureDesktop interface {
	// Available reports whether the helper can be reached.
	Available() bool
	// Input applies one input message of the page (see message).
	Input(data []byte) error
	// Video makes the helper capture the secure desktop and send it as RTP
	// to a loopback port until ctx is done.
	Video(ctx context.Context, v SecureVideo) error
}

// SecureVideo describes the stream asked of the helper.
type SecureVideo struct {
	FPS         int `json:"fps"`
	BitrateKbps int `json:"kbps"`
	Port        int `json:"port"`
	W           int `json:"w"`
	H           int `json:"h"`
}

// Injector applies the page's input messages straight to an input backend.
// The helper uses it; a session goes through input.Service instead, which
// also serialises with the input API.
type Injector struct {
	b       input.Backend
	keys    map[string]input.Key
	buttons map[input.Button]bool
	wx, wy  float64
}

// NewInjector returns an Injector over b.
func NewInjector(b input.Backend) *Injector {
	return &Injector{b: b, keys: map[string]input.Key{}, buttons: map[input.Button]bool{}}
}

// Apply executes one message. Unknown messages and keys are ignored.
func (j *Injector) Apply(data []byte) error {
	if len(data) > maxMessage {
		return nil
	}
	var msg message
	if json.Unmarshal(data, &msg) != nil {
		return nil
	}
	switch msg.T {
	case "mm":
		if msg.X != nil && msg.Y != nil {
			return j.b.Move(*msg.X, *msg.Y)
		}
	case "md", "mu":
		btn, ok := button(msg.B)
		if !ok {
			return nil
		}
		if msg.X != nil && msg.Y != nil {
			if err := j.b.Move(*msg.X, *msg.Y); err != nil {
				return err
			}
		}
		down := msg.T == "md"
		if down {
			j.buttons[btn] = true
		} else {
			delete(j.buttons, btn)
		}
		return j.b.Button(btn, down)
	case "mw":
		j.wx += msg.DX * wheelPerPixel
		j.wy -= msg.DY * wheelPerPixel
		ix, iy := int(j.wx), int(j.wy)
		if fs, ok := j.b.(input.FineScroller); ok {
			j.wx -= float64(ix)
			j.wy -= float64(iy)
			return fs.ScrollDelta(ix, iy)
		}
		nx, ny := ix/input.WheelDelta, iy/input.WheelDelta
		j.wx -= float64(nx * input.WheelDelta)
		j.wy -= float64(ny * input.WheelDelta)
		if nx != 0 || ny != 0 {
			return j.b.Scroll(nx, ny)
		}
	case "kd", "ku":
		k, ok := keyForCode(msg.C)
		if !ok {
			return nil
		}
		down := msg.T == "kd"
		if down {
			j.keys[msg.C] = k
		} else {
			delete(j.keys, msg.C)
		}
		return j.b.Key(k, down)
	case "tx":
		if len(msg.S) > maxText {
			return nil
		}
		for _, r := range msg.S {
			if err := j.b.TypeUnicode(r); err != nil {
				return err
			}
		}
	case "rel":
		j.Release()
	}
	return nil
}

// Release lets go of every key and button still held.
func (j *Injector) Release() {
	for code, k := range j.keys {
		_ = j.b.Key(k, false)
		delete(j.keys, code)
	}
	for btn := range j.buttons {
		_ = j.b.Button(btn, false)
		delete(j.buttons, btn)
	}
}
