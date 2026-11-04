//go:build linux

// Package wayland holds the Wayland support of phase 3 (spec 13.3, D6):
// session detection, screenshots through the xdg-desktop-portal Screenshot
// interface, input through a uinput virtual device, and the portal
// RemoteDesktop interface as a fallback when uinput is not accessible and a
// restore token from an earlier approval exists.
package wayland

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Active reports whether vm-agent runs in a Wayland session: WAYLAND_DISPLAY
// is set and either XDG_SESSION_TYPE says wayland or there is no X display.
// Xwayland's DISPLAY in a Wayland session only reaches X clients, so the
// Wayland backends take precedence there.
func Active() bool {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	return os.Getenv("XDG_SESSION_TYPE") == "wayland" || os.Getenv("DISPLAY") == ""
}

// SocketReady reports whether the compositor's socket accepts connections.
func SocketReady() bool {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return false
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), name)
	}
	c, err := net.DialTimeout("unix", name, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

var (
	stateMu  sync.Mutex
	stateDir string
)

// SetStateDir sets where the RemoteDesktop restore token is kept.
func SetStateDir(dir string) {
	stateMu.Lock()
	stateDir = dir
	stateMu.Unlock()
}

// TokenPath is the file holding the RemoteDesktop restore token.
func TokenPath() string {
	stateMu.Lock()
	defer stateMu.Unlock()
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "wayland-remote-desktop.token")
}

const (
	portalDest = "org.freedesktop.portal.Desktop"
	portalPath = dbus.ObjectPath("/org/freedesktop/portal/desktop")
)

var (
	busMu sync.Mutex
	bus   *dbus.Conn
)

// sessionBus returns a long-lived session bus connection.
func sessionBus() (*dbus.Conn, error) {
	busMu.Lock()
	defer busMu.Unlock()
	if bus != nil && bus.Connected() {
		return bus, nil
	}
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeNoDisplay, "no D-Bus session bus for the desktop portal: %v", err)
	}
	bus = c
	return c, nil
}

func newToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "vmagent" + hex.EncodeToString(b[:])
}

// request calls a portal method that answers through a Request object's
// Response signal (xdg-desktop-portal's request pattern). The match is
// registered before the call on the predictable request path, so the
// signal cannot be missed.
func request(ctx context.Context, conn *dbus.Conn, method string, build func(token string) []any) (map[string]dbus.Variant, error) {
	token := newToken()
	names := conn.Names()
	if len(names) == 0 {
		return nil, errors.New("D-Bus connection has no unique name")
	}
	sender := strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_")
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
	match := []dbus.MatchOption{dbus.WithMatchInterface("org.freedesktop.portal.Request"), dbus.WithMatchMember("Response")}
	if err := conn.AddMatchSignal(match...); err != nil {
		return nil, err
	}
	defer conn.RemoveMatchSignal(match...)
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	var handle dbus.ObjectPath
	if err := conn.Object(portalDest, portalPath).CallWithContext(ctx, method, 0, build(token)...).Store(&handle); err != nil {
		return nil, protocol.Errorf(protocol.CodeIOFailed, "%s: %v", method, err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case s := <-signals:
			if s == nil || (s.Path != path && s.Path != handle) || len(s.Body) < 2 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			results, _ := s.Body[1].(map[string]dbus.Variant)
			switch code {
			case 0:
				return results, nil
			case 1:
				return nil, protocol.Errorf(protocol.CodeIOFailed, "%s was cancelled by the user", method)
			default:
				return nil, protocol.Errorf(protocol.CodeIOFailed, "%s failed (response %d)", method, code)
			}
		}
	}
}

// Screenshot takes a non-interactive screenshot of the whole desktop
// through the portal and returns the PNG file's content; the portal's file
// is deleted afterwards.
func Screenshot(ctx context.Context) (image.Image, error) {
	conn, err := sessionBus()
	if err != nil {
		return nil, err
	}
	results, err := request(ctx, conn, "org.freedesktop.portal.Screenshot.Screenshot", func(token string) []any {
		return []any{"", map[string]dbus.Variant{"handle_token": dbus.MakeVariant(token), "interactive": dbus.MakeVariant(false), "modal": dbus.MakeVariant(false)}}
	})
	if err != nil {
		return nil, err
	}
	raw, _ := results["uri"].Value().(string)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		return nil, protocol.Errorf(protocol.CodeIOFailed, "the screenshot portal returned %q", raw)
	}
	data, err := os.ReadFile(u.Path)
	_ = os.Remove(u.Path)
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode portal screenshot: %w", err)
	}
	rememberSize(img.Bounds().Dx(), img.Bounds().Dy())
	return img, nil
}

// ToRGBA crops img to r (already within its bounds) as an *image.RGBA.
func ToRGBA(img image.Image, r image.Rectangle) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min.Add(img.Bounds().Min), draw.Src)
	return out
}

var (
	sizeMu     sync.Mutex
	sizeW      int
	sizeH      int
	sizeUpdate time.Time
	// sizeFetch is closed when the screenshot taken to learn the size
	// finishes; nil when none is running.
	sizeFetch chan struct{}
	sizeErr   error
)

func rememberSize(w, h int) {
	sizeMu.Lock()
	sizeW, sizeH, sizeUpdate = w, h, time.Now()
	sizeMu.Unlock()
}

// sizeTTL bounds how long a learned desktop size is trusted before another
// screenshot refreshes it (outputs can be added or resized).
const sizeTTL = 30 * time.Second

// fetchTimeout bounds the screenshot that learns the size; it runs on its
// own context so an impatient caller does not cancel it for everyone.
const fetchTimeout = 30 * time.Second

// startFetch starts one background screenshot to learn the size unless one
// is already running, and returns the channel closed when it ends. Callers
// hold sizeMu.
func startFetch() chan struct{} {
	if sizeFetch != nil {
		return sizeFetch
	}
	done := make(chan struct{})
	sizeFetch = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		_, err := Screenshot(ctx)
		sizeMu.Lock()
		sizeErr, sizeFetch = err, nil
		sizeMu.Unlock()
		close(done)
	}()
	return done
}

// Warm starts learning the desktop size in the background, so the first
// probe after startup does not wait for the portal.
func Warm() {
	sizeMu.Lock()
	if sizeW == 0 {
		startFetch()
	}
	sizeMu.Unlock()
}

// ScreenSize returns the desktop size in screenshot pixels. Wayland has no
// portable output-enumeration protocol for clients like vm-agent, so the
// desktop is one display of this size (as agreed for phase 3). A stale size
// is returned at once while a background screenshot refreshes it; without
// any size yet the caller waits for the single in-flight screenshot, up to
// ctx.
func ScreenSize(ctx context.Context) (int, int, error) {
	sizeMu.Lock()
	w, h, fresh := sizeW, sizeH, time.Since(sizeUpdate) < sizeTTL
	if w > 0 {
		if !fresh {
			startFetch()
		}
		sizeMu.Unlock()
		return w, h, nil
	}
	done := startFetch()
	sizeMu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	}
	sizeMu.Lock()
	defer sizeMu.Unlock()
	if sizeW == 0 {
		if sizeErr == nil {
			sizeErr = errors.New("the screenshot portal returned no image")
		}
		return 0, 0, sizeErr
	}
	return sizeW, sizeH, nil
}
