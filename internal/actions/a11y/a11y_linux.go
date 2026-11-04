//go:build linux

package a11y

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/CIPFZ/go-agent-platform/internal/platform/wayland"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

var platform collector = atspi{}

type atspi struct{}

// command re-executes vm-agent as the helper with the request in its
// environment.
func (atspi) command(req Request) (*exec.Cmd, error) {
	if wayland.Active() {
		return nil, protocol.Unsupported("accessibility trees are not available in a Wayland session")
	}
	return helperCommand(req)
}

const (
	atspiRegistry   = "org.a11y.atspi.Registry"
	atspiRoot       = dbus.ObjectPath("/org/a11y/atspi/accessible/root")
	ifaceAccessible = "org.a11y.atspi.Accessible"
	ifaceComponent  = "org.a11y.atspi.Component"
	ifaceText       = "org.a11y.atspi.Text"
	ifaceValue      = "org.a11y.atspi.Value"
	callTimeout     = 2 * time.Second
)

// Available reports whether an AT-SPI bus is reachable from the session.
// In a Wayland session windows cannot be targeted, so it is unavailable
// there (as agreed for phase 3).
func Available() bool {
	if wayland.Active() {
		return false
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var addr string
	return conn.Object("org.a11y.Bus", "/org/a11y/bus").CallWithContext(ctx, "org.a11y.Bus.GetAddress", 0).Store(&addr) == nil && addr != ""
}

func connectA11y() (*dbus.Conn, error) {
	session, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, protocol.Unsupported("no D-Bus session bus for AT-SPI: %v", err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var addr string
	if err := session.Object("org.a11y.Bus", "/org/a11y/bus").CallWithContext(ctx, "org.a11y.Bus.GetAddress", 0).Store(&addr); err != nil {
		return nil, protocol.Unsupported("the AT-SPI bus is not available: %v", err)
	}
	conn, err := dbus.Connect(addr)
	if err != nil {
		return nil, protocol.Unsupported("connect to the AT-SPI bus: %v", err)
	}
	return conn, nil
}

type ref struct {
	Bus  string
	Path dbus.ObjectPath
}

type walker struct {
	conn *dbus.Conn
	req  Request
	b    budget
}

func (w *walker) call(r ref, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return w.conn.Object(r.Bus, r.Path).CallWithContext(ctx, method, 0, args...)
}

func (w *walker) prop(r ref, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := w.call(r, "org.freedesktop.DBus.Properties.Get", iface, name).Store(&v)
	return v, err
}

func (w *walker) children(r ref) []ref {
	var kids []ref
	if err := w.call(r, ifaceAccessible+".GetChildren").Store(&kids); err != nil {
		return nil
	}
	return kids
}

func (w *walker) name(r ref) string {
	v, err := w.prop(r, ifaceAccessible, "Name")
	if err != nil {
		return ""
	}
	s, _ := v.Value().(string)
	return s
}

func (w *walker) states(r ref) uint64 {
	var raw []uint32
	if w.call(r, ifaceAccessible+".GetState").Store(&raw) != nil || len(raw) < 2 {
		return 0
	}
	return uint64(raw[0]) | uint64(raw[1])<<32
}

func (w *walker) busPID(bus string) int {
	var pid uint32
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if w.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, bus).Store(&pid) != nil {
		return 0
	}
	return int(pid)
}

// AT-SPI StateType bits mapped to the names shared with Windows.
var stateBits = []struct {
	bit  uint
	name string
}{
	{1, "active"}, {4, "checked"}, {5, "collapsed"}, {7, "editable"}, {8, "enabled"}, {10, "expanded"},
	{11, "focusable"}, {12, "focused"}, {16, "modal"}, {17, "multi_line"}, {20, "pressed"}, {23, "selected"}, {43, "read_only"},
}

const (
	stateActive  = 1
	stateShowing = 25
)

func stateNames(bits uint64) []string {
	out := []string{}
	for _, s := range stateBits {
		if bits&(1<<s.bit) != 0 {
			out = append(out, s.name)
		}
	}
	if bits&(1<<stateShowing) == 0 {
		out = append(out, "offscreen")
	}
	sort.Strings(out)
	return out
}

// findWindow locates the accessible top-level of the target window: among
// the applications whose bus process is the window's pid, the top-level
// whose name equals the title, else the active one, else the first.
func (w *walker) findWindow() (ref, error) {
	root := ref{atspiRegistry, atspiRoot}
	var candidates []ref
	for _, app := range w.children(root) {
		if w.req.Target.PID != 0 && w.busPID(app.Bus) != w.req.Target.PID {
			continue
		}
		candidates = append(candidates, w.children(app)...)
	}
	if len(candidates) == 0 {
		return ref{}, protocol.Errorf(protocol.CodeNotFound, "no accessible application for window %s (pid %d); is its toolkit accessibility enabled?", w.req.Target.ID, w.req.Target.PID)
	}
	for _, c := range candidates {
		if w.req.Target.Title != "" && w.name(c) == w.req.Target.Title {
			return c, nil
		}
	}
	for _, c := range candidates {
		if w.states(c)&(1<<stateActive) != 0 {
			return c, nil
		}
	}
	return candidates[0], nil
}

func (w *walker) node(r ref, depth int) *Node {
	n := &Node{Name: w.name(r), State: stateNames(w.states(r)), Children: []*Node{}}
	var role string
	if w.call(r, ifaceAccessible+".GetRoleName").Store(&role) == nil {
		n.Role = snake(role)
	}
	var ifaces []string
	_ = w.call(r, ifaceAccessible+".GetInterfaces").Store(&ifaces)
	has := map[string]bool{}
	for _, i := range ifaces {
		has[i] = true
	}
	if has[ifaceComponent] {
		// GetExtents returns one (iiii) struct; coord type 0 is screen.
		var ext struct{ X, Y, W, H int32 }
		if w.call(r, ifaceComponent+".GetExtents", uint32(0)).Store(&ext) == nil && ext.W > 0 && ext.H > 0 {
			n.Rect = &protocol.Rect{X: int(ext.X) - w.req.OriginX, Y: int(ext.Y) - w.req.OriginY, W: int(ext.W), H: int(ext.H)}
		}
	}
	switch {
	case has[ifaceText]:
		var text string
		if w.call(r, ifaceText+".GetText", int32(0), int32(maxValueRunes)).Store(&text) == nil {
			n.Value = clip(text)
		}
	case has[ifaceValue]:
		if v, err := w.prop(r, ifaceValue, "CurrentValue"); err == nil {
			n.Value = fmt.Sprint(v.Value())
		}
	}
	if depth >= w.b.maxDepth {
		if len(w.children(r)) > 0 {
			w.b.truncated = true
		}
		return n
	}
	for _, c := range w.children(r) {
		if !w.b.take() {
			break
		}
		n.Children = append(n.Children, w.node(c, depth+1))
	}
	return n
}

func walk(req Request) (*Result, error) {
	conn, err := connectA11y()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	w := &walker{conn: conn, req: req, b: budget{maxDepth: req.MaxDepth, maxNodes: req.MaxNodes}}
	top, err := w.findWindow()
	if err != nil {
		return nil, err
	}
	w.b.take()
	root := w.node(top, 1)
	return &Result{Root: root, Nodes: w.b.nodes, Truncated: w.b.truncated}, nil
}
