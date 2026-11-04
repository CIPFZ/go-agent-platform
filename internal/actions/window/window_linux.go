//go:build linux

package window

import (
	"time"
	"unicode/utf8"

	"github.com/jezek/xgb/xproto"

	"github.com/CIPFZ/go-agent-platform/internal/platform/wayland"
	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func newBackend() Backend {
	if wayland.Active() {
		// Wayland has no protocol for clients to manage other windows.
		return unsupportedBackend{"window management is not available in a Wayland session"}
	}
	return ewmhBackend{}
}

// ewmhBackend manages windows through the EWMH hints of the running window
// manager: _NET_CLIENT_LIST_STACKING for enumeration in Z order,
// _NET_ACTIVE_WINDOW, _NET_CLOSE_WINDOW, _NET_WM_STATE and
// _NET_MOVERESIZE_WINDOW (spec section 10). Without a window manager the
// root's mapped children are listed and core requests are used.
type ewmhBackend struct{}

func conn() (*x11.Conn, error) {
	c, err := x11.Shared()
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeNoDisplay, "%v", err)
	}
	return c, nil
}

func parseXID(id string) (xproto.Window, error) {
	v, err := ParseID(id)
	if err != nil || v == 0 || v > 0xffffffff {
		return 0, protocol.Errorf(protocol.CodeInvalidRequest, "invalid window id %q", id)
	}
	return xproto.Window(v), nil
}

// shellTypes and toolTypes classify _NET_WM_WINDOW_TYPE values.
var (
	shellTypes = map[string]bool{"_NET_WM_WINDOW_TYPE_DESKTOP": true, "_NET_WM_WINDOW_TYPE_DOCK": true}
	toolTypes  = map[string]bool{
		"_NET_WM_WINDOW_TYPE_TOOLBAR": true, "_NET_WM_WINDOW_TYPE_MENU": true, "_NET_WM_WINDOW_TYPE_UTILITY": true,
		"_NET_WM_WINDOW_TYPE_SPLASH": true, "_NET_WM_WINDOW_TYPE_DROPDOWN_MENU": true, "_NET_WM_WINDOW_TYPE_POPUP_MENU": true,
		"_NET_WM_WINDOW_TYPE_TOOLTIP": true, "_NET_WM_WINDOW_TYPE_NOTIFICATION": true, "_NET_WM_WINDOW_TYPE_COMBO": true,
		"_NET_WM_WINDOW_TYPE_DND": true,
	}
)

func stacking(c *x11.Conn) ([]xproto.Window, bool, error) {
	for _, prop := range []string{"_NET_CLIENT_LIST_STACKING", "_NET_CLIENT_LIST"} {
		reply, err := c.Property(c.Root, prop)
		if err != nil {
			return nil, false, err
		}
		if ids := x11.PropertyCardinals(reply); reply != nil && reply.Format == 32 {
			out := make([]xproto.Window, len(ids))
			for i, id := range ids {
				out[i] = xproto.Window(id)
			}
			return out, true, nil
		}
	}
	tree, err := xproto.QueryTree(c.X, c.Root).Reply()
	if err != nil {
		return nil, false, c.Check(err)
	}
	return tree.Children, false, nil
}

func (ewmhBackend) List(visibleOnly bool) ([]Window, error) {
	c, err := conn()
	if err != nil {
		return nil, err
	}
	ids, managed, err := stacking(c)
	if err != nil {
		return nil, err
	}
	ox, oy, err := c.Origin()
	if err != nil {
		return nil, err
	}
	var active xproto.Window
	if reply, err := c.Property(c.Root, "_NET_ACTIVE_WINDOW"); err == nil {
		if v := x11.PropertyCardinals(reply); len(v) > 0 {
			active = xproto.Window(v[0])
		}
	}
	if active == 0 {
		if focus, err := xproto.GetInputFocus(c.X).Reply(); err == nil {
			active = focus.Focus
		}
	}
	out := make([]Window, 0, len(ids))
	// Stacking order is bottom to top; the API lists topmost first.
	for i := len(ids) - 1; i >= 0; i-- {
		w, ok := describe(c, ids[i], ox, oy, managed)
		if !ok {
			continue
		}
		w.Active = ids[i] == active
		if visibleOnly && !w.Visible {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

func textProperty(c *x11.Conn, w xproto.Window, names ...string) string {
	for _, name := range names {
		reply, err := c.Property(w, name)
		if err == nil && reply != nil && len(reply.Value) > 0 {
			v := string(reply.Value)
			if !utf8.ValidString(v) {
				// WM_NAME is ISO 8859-1 when its type is STRING.
				runes := make([]rune, len(reply.Value))
				for i, b := range reply.Value {
					runes[i] = rune(b)
				}
				v = string(runes)
			}
			return v
		}
	}
	return ""
}

func atomNames(c *x11.Conn, w xproto.Window, prop string) map[string]bool {
	out := map[string]bool{}
	reply, err := c.Property(w, prop)
	if err != nil {
		return out
	}
	for _, a := range x11.PropertyCardinals(reply) {
		if name, err := c.AtomName(xproto.Atom(a)); err == nil {
			out[name] = true
		}
	}
	return out
}

func describe(c *x11.Conn, id xproto.Window, ox, oy int, managed bool) (Window, bool) {
	attrs, err := xproto.GetWindowAttributes(c.X, id).Reply()
	if err != nil {
		return Window{}, false // destroyed meanwhile
	}
	if !managed && attrs.OverrideRedirect {
		return Window{}, false
	}
	w := Window{ID: FormatID(uint64(id))}
	w.Title = textProperty(c, id, "_NET_WM_NAME", "WM_NAME")
	if reply, err := c.Property(id, "WM_CLASS"); err == nil && reply != nil {
		// WM_CLASS is "instance\0class\0".
		parts := splitNul(reply.Value)
		if len(parts) >= 2 {
			w.Class = parts[1]
		} else if len(parts) == 1 {
			w.Class = parts[0]
		}
	}
	if reply, err := c.Property(id, "_NET_WM_PID"); err == nil {
		if v := x11.PropertyCardinals(reply); len(v) > 0 {
			w.PID = int(v[0])
		}
	}
	states := atomNames(c, id, "_NET_WM_STATE")
	w.Minimized = states["_NET_WM_STATE_HIDDEN"]
	if reply, err := c.Property(id, "WM_STATE"); err == nil {
		if v := x11.PropertyCardinals(reply); len(v) > 0 && v[0] == 3 { // IconicState
			w.Minimized = true
		}
	}
	w.Maximized = states["_NET_WM_STATE_MAXIMIZED_VERT"] && states["_NET_WM_STATE_MAXIMIZED_HORZ"]
	types := atomNames(c, id, "_NET_WM_WINDOW_TYPE")
	for t := range types {
		if shellTypes[t] {
			w.Shell = true
		}
		if toolTypes[t] {
			w.Tool = true
		}
	}
	w.Visible = w.Minimized || attrs.MapState == xproto.MapStateViewable
	if !w.Minimized && attrs.MapState == xproto.MapStateViewable {
		if client, frame, ok := geometry(c, id, ox, oy); ok {
			w.ClientRect, w.Rect = &client, &frame
		}
	}
	return w, true
}

func splitNul(b []byte) []string {
	var out []string
	start := 0
	for i, ch := range b {
		if ch == 0 {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

// geometry returns the client rectangle and the whole-window rectangle
// (client plus _NET_FRAME_EXTENTS) in API coordinates.
func geometry(c *x11.Conn, id xproto.Window, ox, oy int) (client, frame protocol.Rect, ok bool) {
	g, err := xproto.GetGeometry(c.X, xproto.Drawable(id)).Reply()
	if err != nil {
		return client, frame, false
	}
	t, err := xproto.TranslateCoordinates(c.X, id, c.Root, 0, 0).Reply()
	if err != nil {
		return client, frame, false
	}
	client = protocol.Rect{X: int(t.DstX) - ox, Y: int(t.DstY) - oy, W: int(g.Width), H: int(g.Height)}
	frame = client
	if reply, err := c.Property(id, "_NET_FRAME_EXTENTS"); err == nil {
		if e := x11.PropertyCardinals(reply); len(e) == 4 {
			l, r, top, b := int(e[0]), int(e[1]), int(e[2]), int(e[3])
			frame = protocol.Rect{X: client.X - l, Y: client.Y - top, W: client.W + l + r, H: client.H + top + b}
		}
	}
	return client, frame, true
}

func wmSupports(c *x11.Conn, hint string) bool {
	return atomNames(c, c.Root, "_NET_SUPPORTED")[hint]
}

func activeWindow(c *x11.Conn) xproto.Window {
	if reply, err := c.Property(c.Root, "_NET_ACTIVE_WINDOW"); err == nil {
		if v := x11.PropertyCardinals(reply); len(v) > 0 {
			return xproto.Window(v[0])
		}
	}
	return 0
}

func (ewmhBackend) Activate(id string) error {
	c, err := conn()
	if err != nil {
		return err
	}
	w, err := parseXID(id)
	if err != nil {
		return err
	}
	if wmSupports(c, "_NET_ACTIVE_WINDOW") {
		// Source indication 2: a pager/automation request, which window
		// managers honour without focus-stealing prevention.
		if err := c.SendClientMessage(w, "_NET_ACTIVE_WINDOW", 2, 0, 0); err != nil {
			return err
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if activeWindow(c) == w {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		return protocol.Errorf(protocol.CodeIOFailed, "the window manager did not activate window %s", id).WithReason(protocol.ReasonForegroundLock)
	}
	// No EWMH window manager: map, raise and focus with core requests.
	_ = xproto.MapWindowChecked(c.X, w).Check()
	_ = xproto.ConfigureWindowChecked(c.X, w, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove}).Check()
	if err := xproto.SetInputFocusChecked(c.X, xproto.InputFocusPointerRoot, w, xproto.TimeCurrentTime).Check(); err != nil {
		return protocol.Errorf(protocol.CodeIOFailed, "set input focus: %v", c.Check(err)).WithReason(protocol.ReasonForegroundLock)
	}
	return c.Sync()
}

func (ewmhBackend) Close(id string) error {
	c, err := conn()
	if err != nil {
		return err
	}
	w, err := parseXID(id)
	if err != nil {
		return err
	}
	if wmSupports(c, "_NET_CLOSE_WINDOW") {
		return c.SendClientMessage(w, "_NET_CLOSE_WINDOW", 0, 2)
	}
	// ICCCM WM_DELETE_WINDOW sent to the client itself.
	protocols := atomNames(c, w, "WM_PROTOCOLS")
	if !protocols["WM_DELETE_WINDOW"] {
		return protocol.Unsupported("window %s does not accept close requests and no window manager is running", id)
	}
	wmProtocols, err := c.Atom("WM_PROTOCOLS")
	if err != nil {
		return err
	}
	del, err := c.Atom("WM_DELETE_WINDOW")
	if err != nil {
		return err
	}
	ev := xproto.ClientMessageEvent{Format: 32, Window: w, Type: wmProtocols,
		Data: xproto.ClientMessageDataUnionData32New([]uint32{uint32(del), 0, 0, 0, 0})}
	return c.Check(xproto.SendEventChecked(c.X, false, w, xproto.EventMaskNoEvent, string(ev.Bytes())).Check())
}

const (
	netWMStateRemove = 0
	netWMStateAdd    = 1
	iconicState      = 3
)

func (b ewmhBackend) SetState(id string, state State) error {
	c, err := conn()
	if err != nil {
		return err
	}
	w, err := parseXID(id)
	if err != nil {
		return err
	}
	atoms, err := c.Atoms("_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ")
	if err != nil {
		return err
	}
	switch state {
	case StateMaximize:
		return c.SendClientMessage(w, "_NET_WM_STATE", netWMStateAdd, uint32(atoms[0]), uint32(atoms[1]), 2)
	case StateMinimize:
		return c.SendClientMessage(w, "WM_CHANGE_STATE", iconicState)
	case StateRestore:
		if err := c.SendClientMessage(w, "_NET_WM_STATE", netWMStateRemove, uint32(atoms[0]), uint32(atoms[1]), 2); err != nil {
			return err
		}
		desc, ok := describe(c, w, 0, 0, true)
		if ok && desc.Minimized {
			// De-iconify: activation maps the window again.
			if wmSupports(c, "_NET_ACTIVE_WINDOW") {
				return c.SendClientMessage(w, "_NET_ACTIVE_WINDOW", 2, 0, 0)
			}
			return c.Check(xproto.MapWindowChecked(c.X, w).Check())
		}
		return nil
	}
	return protocol.Errorf(protocol.CodeInvalidRequest, "state must be maximize, minimize or restore")
}

func (ewmhBackend) SetRect(id string, r protocol.Rect) error {
	c, err := conn()
	if err != nil {
		return err
	}
	w, err := parseXID(id)
	if err != nil {
		return err
	}
	ox, oy, err := c.Origin()
	if err != nil {
		return err
	}
	var l, right, top, bottom int
	if reply, err := c.Property(w, "_NET_FRAME_EXTENTS"); err == nil {
		if e := x11.PropertyCardinals(reply); len(e) == 4 {
			l, right, top, bottom = int(e[0]), int(e[1]), int(e[2]), int(e[3])
		}
	}
	cw, ch := r.W-l-right, r.H-top-bottom
	if cw < 1 || ch < 1 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "rect is smaller than the window frame")
	}
	x, y := r.X+ox, r.Y+oy
	if wmSupports(c, "_NET_MOVERESIZE_WINDOW") {
		// Gravity NorthWest (1), x/y/width/height present (bits 8-11),
		// source indication 2 (bits 12-13): x/y place the frame's top-left.
		flags := uint32(1) | 0xf<<8 | 2<<12
		return c.SendClientMessage(w, "_NET_MOVERESIZE_WINDOW", flags, uint32(int32(x)), uint32(int32(y)), uint32(cw), uint32(ch))
	}
	mask := uint16(xproto.ConfigWindowX | xproto.ConfigWindowY | xproto.ConfigWindowWidth | xproto.ConfigWindowHeight)
	return c.Check(xproto.ConfigureWindowChecked(c.X, w, mask, []uint32{uint32(int32(x + l)), uint32(int32(y + top)), uint32(cw), uint32(ch)}).Check())
}
