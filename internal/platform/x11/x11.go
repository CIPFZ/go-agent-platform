//go:build linux

// Package x11 holds the X11 plumbing shared by the Linux screen, input,
// window and clipboard backends: a long-lived connection that is redialled
// when the server goes away, an atom cache, monitor enumeration and the
// translation between root-window coordinates and the API coordinate system
// (physical pixels, origin at the primary monitor's top-left corner, spec
// section 6.2).
package x11

import (
	"errors"
	"io"
	"log"
	"strings"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xinerama"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

func init() {
	// xgb logs every recoverable condition (missing .Xauthority, read errors);
	// failures surface as errors to callers instead.
	xgb.Logger = log.New(io.Discard, "", 0)
}

// ErrNoDisplay means no X server could be reached.
var ErrNoDisplay = errors.New("X11 display is not available")

// Conn is one connection to the X server.
type Conn struct {
	X      *xgb.Conn
	Setup  *xproto.SetupInfo
	Screen *xproto.ScreenInfo
	Root   xproto.Window

	HasXTest, HasXFixes, HasRandR, HasXinerama, HasShm bool

	dead     chan struct{}
	deadOnce sync.Once

	atomMu sync.Mutex
	atoms  map[string]xproto.Atom
	names  map[xproto.Atom]string
}

// Dial opens a connection to $DISPLAY. Every event and asynchronous error is
// passed to onEvent (nil discards them) from a dedicated goroutine; xgb stops
// delivering replies once its event queue is full, so events must always be
// drained.
func Dial(onEvent func(xgb.Event)) (*Conn, error) {
	x, err := xgb.NewConn()
	if err != nil {
		return nil, errors.Join(ErrNoDisplay, err)
	}
	c := &Conn{X: x, dead: make(chan struct{}), atoms: map[string]xproto.Atom{}, names: map[xproto.Atom]string{}}
	c.Setup = xproto.Setup(x)
	c.Screen = c.Setup.DefaultScreen(x)
	c.Root = c.Screen.Root
	c.HasXTest = xtest.Init(x) == nil
	c.HasXFixes = xfixes.Init(x) == nil
	if c.HasXFixes {
		// XFixes requires a version handshake before cursor requests.
		_, err := xfixes.QueryVersion(x, 4, 0).Reply()
		c.HasXFixes = err == nil
	}
	c.HasRandR = randr.Init(x) == nil
	if c.HasRandR {
		_, err := randr.QueryVersion(x, 1, 5).Reply()
		c.HasRandR = err == nil
	}
	c.HasXinerama = xinerama.Init(x) == nil
	c.HasShm = shm.Init(x) == nil
	go c.drain(onEvent)
	return c, nil
}

func (c *Conn) drain(onEvent func(xgb.Event)) {
	for {
		ev, xerr := c.X.WaitForEvent()
		if ev == nil && xerr == nil {
			c.markDead()
			return
		}
		if ev != nil && onEvent != nil {
			onEvent(ev)
		}
	}
}

func (c *Conn) markDead() { c.deadOnce.Do(func() { close(c.dead) }) }

// Alive reports whether the connection is still usable.
func (c *Conn) Alive() bool {
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

// Dead is closed when the connection is lost.
func (c *Conn) Dead() <-chan struct{} { return c.dead }

// Close closes the connection.
func (c *Conn) Close() {
	c.markDead()
	c.X.Close()
}

// Check inspects err from a request on c and marks the connection dead when
// the transport failed, so the next Shared call redials.
func (c *Conn) Check(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") {
		c.markDead()
		return errors.Join(ErrNoDisplay, err)
	}
	return err
}

// Sync performs a round trip, guaranteeing every earlier request has been
// processed by the server.
func (c *Conn) Sync() error {
	_, err := xproto.GetInputFocus(c.X).Reply()
	return c.Check(err)
}

var (
	sharedMu sync.Mutex
	shared   *Conn
)

// Shared returns the process-wide connection used for queries and input,
// dialling or redialling as needed.
func Shared() (*Conn, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared != nil && shared.Alive() {
		return shared, nil
	}
	if shared != nil {
		shared.Close()
		shared = nil
	}
	c, err := Dial(nil)
	if err != nil {
		return nil, err
	}
	shared = c
	return c, nil
}

// Atom interns name, caching the result.
func (c *Conn) Atom(name string) (xproto.Atom, error) {
	c.atomMu.Lock()
	if a, ok := c.atoms[name]; ok {
		c.atomMu.Unlock()
		return a, nil
	}
	c.atomMu.Unlock()
	reply, err := xproto.InternAtom(c.X, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, c.Check(err)
	}
	c.atomMu.Lock()
	c.atoms[name] = reply.Atom
	c.names[reply.Atom] = name
	c.atomMu.Unlock()
	return reply.Atom, nil
}

// MustAtoms interns several atoms at once.
func (c *Conn) Atoms(names ...string) ([]xproto.Atom, error) {
	out := make([]xproto.Atom, len(names))
	for i, n := range names {
		a, err := c.Atom(n)
		if err != nil {
			return nil, err
		}
		out[i] = a
	}
	return out, nil
}

// AtomName returns the name of a, caching the result.
func (c *Conn) AtomName(a xproto.Atom) (string, error) {
	c.atomMu.Lock()
	if n, ok := c.names[a]; ok {
		c.atomMu.Unlock()
		return n, nil
	}
	c.atomMu.Unlock()
	reply, err := xproto.GetAtomName(c.X, a).Reply()
	if err != nil {
		return "", c.Check(err)
	}
	c.atomMu.Lock()
	c.names[a] = reply.Name
	c.atoms[reply.Name] = a
	c.atomMu.Unlock()
	return reply.Name, nil
}

// Property reads a whole property of w. A missing property returns nil data
// and no error.
func (c *Conn) Property(w xproto.Window, name string) (*xproto.GetPropertyReply, error) {
	atom, err := c.Atom(name)
	if err != nil {
		return nil, err
	}
	reply, err := xproto.GetProperty(c.X, false, w, atom, xproto.GetPropertyTypeAny, 0, 1<<24).Reply()
	if err != nil {
		return nil, c.Check(err)
	}
	return reply, nil
}

// PropertyCardinals decodes a 32-bit property as a list of values.
func PropertyCardinals(reply *xproto.GetPropertyReply) []uint32 {
	if reply == nil || reply.Format != 32 {
		return nil
	}
	out := make([]uint32, 0, len(reply.Value)/4)
	for i := 0; i+4 <= len(reply.Value); i += 4 {
		out = append(out, xgb.Get32(reply.Value[i:]))
	}
	return out
}

// SendClientMessage sends a 32-bit format client message about w to the root
// window, the way EWMH requests are delivered to the window manager.
func (c *Conn) SendClientMessage(w xproto.Window, msgType string, data ...uint32) error {
	atom, err := c.Atom(msgType)
	if err != nil {
		return err
	}
	var d [5]uint32
	copy(d[:], data)
	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: w,
		Type:   atom,
		Data:   xproto.ClientMessageDataUnionData32New(d[:]),
	}
	mask := uint32(xproto.EventMaskSubstructureRedirect | xproto.EventMaskSubstructureNotify)
	return c.Check(xproto.SendEventChecked(c.X, false, c.Root, mask, string(ev.Bytes())).Check())
}
