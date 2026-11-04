//go:build linux

package wayland

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// RemoteDesktop is a started xdg-desktop-portal RemoteDesktop session with a
// ScreenCast stream, used for input when uinput is not accessible. The
// first session needs a person to approve the compositor's dialog; it is
// created with persist_mode 2 and the returned restore token is stored, so
// later sessions start without a dialog (spec 13.3, as agreed: used only
// when a token exists).
type RemoteDesktop struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	session dbus.ObjectPath
	stream  uint32
	// Stream geometry in compositor logical coordinates.
	sx, sy, sw, sh int32
}

const (
	deviceKeyboard      = 1
	devicePointer       = 2
	sourceMonitor       = 1
	persistUntilRevoked = 2
)

// ErrNoToken means no restore token is stored.
var ErrNoToken = errors.New("no RemoteDesktop restore token; run the one-time approval (VM_AGENT_PORTAL_SETUP=1 vm-agent)")

func readToken() (string, error) {
	path := TokenPath()
	if path == "" {
		return "", ErrNoToken
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return "", ErrNoToken
	}
	return strings.TrimSpace(string(data)), nil
}

func writeToken(token string) error {
	path := TokenPath()
	if path == "" || token == "" {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// TokenExists reports whether a restore token is stored.
func TokenExists() bool { _, err := readToken(); return err == nil }

// StartRemoteDesktop creates and starts a session. With a stored token it
// restores the earlier approval; without one (setup only) the compositor
// shows its approval dialog and ctx must allow for a person to answer.
// The token in the response replaces the stored one.
func StartRemoteDesktop(ctx context.Context, requireToken bool) (*RemoteDesktop, error) {
	token, err := readToken()
	if err != nil && requireToken {
		return nil, err
	}
	conn, err := sessionBus()
	if err != nil {
		return nil, err
	}
	res, err := request(ctx, conn, "org.freedesktop.portal.RemoteDesktop.CreateSession", func(t string) []any {
		return []any{map[string]dbus.Variant{"handle_token": dbus.MakeVariant(t), "session_handle_token": dbus.MakeVariant(t)}}
	})
	if err != nil {
		return nil, err
	}
	sessionStr, _ := res["session_handle"].Value().(string)
	session := dbus.ObjectPath(sessionStr)
	if !session.IsValid() {
		if p, ok := res["session_handle"].Value().(dbus.ObjectPath); ok {
			session = p
		}
	}
	if !session.IsValid() {
		return nil, protocol.Errorf(protocol.CodeIOFailed, "RemoteDesktop returned no session")
	}
	devOpts := func(t string) map[string]dbus.Variant {
		o := map[string]dbus.Variant{"handle_token": dbus.MakeVariant(t), "types": dbus.MakeVariant(uint32(deviceKeyboard | devicePointer)),
			"persist_mode": dbus.MakeVariant(uint32(persistUntilRevoked))}
		if token != "" {
			o["restore_token"] = dbus.MakeVariant(token)
		}
		return o
	}
	if _, err := request(ctx, conn, "org.freedesktop.portal.RemoteDesktop.SelectDevices", func(t string) []any {
		return []any{session, devOpts(t)}
	}); err != nil {
		return nil, err
	}
	// Absolute pointer motion is relative to a ScreenCast stream.
	if _, err := request(ctx, conn, "org.freedesktop.portal.ScreenCast.SelectSources", func(t string) []any {
		return []any{session, map[string]dbus.Variant{"handle_token": dbus.MakeVariant(t), "types": dbus.MakeVariant(uint32(sourceMonitor)), "multiple": dbus.MakeVariant(false)}}
	}); err != nil {
		return nil, err
	}
	started, err := request(ctx, conn, "org.freedesktop.portal.RemoteDesktop.Start", func(t string) []any {
		return []any{session, "", map[string]dbus.Variant{"handle_token": dbus.MakeVariant(t)}}
	})
	if err != nil {
		return nil, err
	}
	if newToken, _ := started["restore_token"].Value().(string); newToken != "" {
		_ = writeToken(newToken)
	}
	rd := &RemoteDesktop{conn: conn, session: session}
	// streams is a(ua{sv}), which godbus decodes as [][]any.
	if streams, ok := started["streams"].Value().([][]any); ok && len(streams) > 0 {
		rd.parseStream(streams[0])
	}
	if rd.stream == 0 {
		return nil, protocol.Errorf(protocol.CodeIOFailed, "RemoteDesktop started without a screen stream")
	}
	return rd, nil
}

func (r *RemoteDesktop) parseStream(fields []any) {
	if len(fields) < 2 {
		return
	}
	r.stream, _ = fields[0].(uint32)
	if props, ok := fields[1].(map[string]dbus.Variant); ok {
		r.props(props)
	}
}

func (r *RemoteDesktop) props(props map[string]dbus.Variant) {
	if v, ok := props["position"].Value().([]any); ok && len(v) == 2 {
		r.sx, _ = v[0].(int32)
		r.sy, _ = v[1].(int32)
	}
	if v, ok := props["size"].Value().([]any); ok && len(v) == 2 {
		r.sw, _ = v[0].(int32)
		r.sh, _ = v[1].(int32)
	}
}

func (r *RemoteDesktop) notify(method string, args ...any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := append([]any{r.session, map[string]dbus.Variant{}}, args...)
	if err := r.conn.Object(portalDest, portalPath).Call("org.freedesktop.portal.RemoteDesktop."+method, 0, all...).Err; err != nil {
		return protocol.Errorf(protocol.CodeIOFailed, "RemoteDesktop %s: %v", method, err)
	}
	return nil
}

// Move moves the pointer to desktop pixel (x, y) of a screenshot that is
// w x h pixels, scaling into the stream's logical size.
func (r *RemoteDesktop) Move(x, y, w, h int) error {
	fx, fy := float64(x), float64(y)
	if r.sw > 0 && w > 0 {
		fx = float64(x) * float64(r.sw) / float64(w)
		fy = float64(y) * float64(r.sh) / float64(h)
	}
	return r.notify("NotifyPointerMotionAbsolute", r.stream, fx, fy)
}

// Button presses or releases an evdev button code.
func (r *RemoteDesktop) Button(code uint16, down bool) error {
	return r.notify("NotifyPointerButton", int32(code), state(down))
}

// Wheel scrolls by discrete steps; the portal's vertical axis counts down.
func (r *RemoteDesktop) Wheel(dx, dy int) error {
	if dy != 0 {
		if err := r.notify("NotifyPointerAxisDiscrete", uint32(0), int32(-dy)); err != nil {
			return err
		}
	}
	if dx != 0 {
		return r.notify("NotifyPointerAxisDiscrete", uint32(1), int32(dx))
	}
	return nil
}

// Key presses or releases an evdev key code.
func (r *RemoteDesktop) Key(code uint16, down bool) error {
	return r.notify("NotifyKeyboardKeycode", int32(code), state(down))
}

// Keysym presses or releases a keysym, which the compositor maps to the
// active layout; any Unicode character works this way.
func (r *RemoteDesktop) Keysym(sym uint32, down bool) error {
	return r.notify("NotifyKeyboardKeysym", int32(sym), state(down))
}

// Close ends the session.
func (r *RemoteDesktop) Close() {
	_ = r.conn.Object(portalDest, r.session).Call("org.freedesktop.portal.Session.Close", 0).Err
}

func state(down bool) uint32 {
	if down {
		return 1
	}
	return 0
}

// SetupMain runs the one-time RemoteDesktop approval: it starts a session
// without a token, waits up to five minutes for a person to approve the
// compositor's dialog, stores the restore token and exits. vm-agent runs it
// when started with VM_AGENT_PORTAL_SETUP=1.
func SetupMain(dir string) int {
	SetStateDir(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rd, err := StartRemoteDesktop(ctx, false)
	if err != nil {
		os.Stderr.WriteString("vm-agent: RemoteDesktop setup failed: " + err.Error() + "\n")
		return 1
	}
	rd.Close()
	if !TokenExists() {
		os.Stderr.WriteString("vm-agent: the portal returned no restore token; this compositor cannot persist the approval\n")
		return 1
	}
	os.Stderr.WriteString("vm-agent: RemoteDesktop approval stored in " + TokenPath() + "\n")
	return 0
}
