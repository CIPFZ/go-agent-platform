//go:build linux

package wayland

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/godbus/dbus/v5"
)

func TestStructLayouts(t *testing.T) {
	// linux/uinput.h sizes on 64-bit, which the ioctl numbers encode.
	if s := unsafe.Sizeof(uinputSetup{}); s != 92 {
		t.Fatalf("uinput_setup = %d bytes", s)
	}
	if s := unsafe.Sizeof(uinputAbsSetup{}); s != 28 {
		t.Fatalf("uinput_abs_setup = %d bytes", s)
	}
	if uiDevSetup != 0x405c5503 || uiAbsSetup != 0x401c5504 || uiSetEvBit != 0x40045564 || uiDevCreate != 0x5501 {
		t.Fatalf("ioctl numbers %x %x %x %x", uiDevSetup, uiAbsSetup, uiSetEvBit, uiDevCreate)
	}
}

func TestUSLayout(t *testing.T) {
	for r, want := range map[rune][2]any{'a': {uint16(30), false}, 'A': {uint16(30), true}, '!': {uint16(2), true}, ' ': {uint16(57), false}, '~': {uint16(41), true}, '?': {uint16(53), true}} {
		code, shift, ok := LayoutKey(r)
		if !ok || code != want[0].(uint16) || shift != want[1].(bool) {
			t.Errorf("LayoutKey(%q) = %d %v %v", r, code, shift, ok)
		}
	}
	if _, _, ok := LayoutKey('é'); ok {
		t.Error("non-ASCII mapped")
	}
}

func TestActive(t *testing.T) {
	for _, c := range []struct {
		wayland, display, sessionType string
		want                          bool
	}{{"wayland-0", "", "", true}, {"wayland-0", ":0", "wayland", true}, {"wayland-0", ":0", "x11", false}, {"", ":0", "wayland", false}} {
		t.Setenv("WAYLAND_DISPLAY", c.wayland)
		t.Setenv("DISPLAY", c.display)
		t.Setenv("XDG_SESSION_TYPE", c.sessionType)
		if got := Active(); got != c.want {
			t.Errorf("%+v: Active = %v", c, got)
		}
	}
}

// fakePortal implements the Screenshot and RemoteDesktop request flows on
// a private session bus.
type fakePortal struct {
	conn     *dbus.Conn
	dir      string
	mu       sync.Mutex
	calls    []string
	notifies []string
	token    string // restore token the portal hands out
	gotToken string // restore token the client sent
}

func (p *fakePortal) respond(sender dbus.Sender, token string, results map[string]dbus.Variant) dbus.ObjectPath {
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + strings.ReplaceAll(strings.TrimPrefix(string(sender), ":"), ".", "_") + "/" + token)
	go func() {
		time.Sleep(20 * time.Millisecond)
		p.conn.Emit(path, "org.freedesktop.portal.Request.Response", uint32(0), results)
	}()
	return path
}

func tok(opts map[string]dbus.Variant) string {
	s, _ := opts["handle_token"].Value().(string)
	return s
}

type screenshotIface struct{ p *fakePortal }

func (s screenshotIface) Screenshot(sender dbus.Sender, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	img.SetRGBA(10, 10, color.RGBA{255, 0, 0, 255})
	path := filepath.Join(s.p.dir, "shot.png")
	f, _ := os.Create(path)
	png.Encode(f, img)
	f.Close()
	return s.p.respond(sender, tok(opts), map[string]dbus.Variant{"uri": dbus.MakeVariant("file://" + path)}), nil
}

type remoteIface struct{ p *fakePortal }

func (r remoteIface) log(name string) {
	r.p.mu.Lock()
	r.p.calls = append(r.p.calls, name)
	r.p.mu.Unlock()
}

func (r remoteIface) CreateSession(sender dbus.Sender, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	r.log("CreateSession")
	return r.p.respond(sender, tok(opts), map[string]dbus.Variant{"session_handle": dbus.MakeVariant("/org/freedesktop/portal/desktop/session/x/s1")}), nil
}

func (r remoteIface) SelectDevices(sender dbus.Sender, session dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	r.log("SelectDevices")
	r.p.gotToken, _ = opts["restore_token"].Value().(string)
	return r.p.respond(sender, tok(opts), map[string]dbus.Variant{}), nil
}

func (r remoteIface) Start(sender dbus.Sender, session dbus.ObjectPath, parent string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	r.log("Start")
	streams := []struct {
		Node  uint32
		Props map[string]dbus.Variant
	}{{42, map[string]dbus.Variant{"position": dbus.MakeVariant(struct{ X, Y int32 }{0, 0}), "size": dbus.MakeVariant(struct{ W, H int32 }{32, 24})}}}
	return r.p.respond(sender, tok(opts), map[string]dbus.Variant{"devices": dbus.MakeVariant(uint32(3)), "streams": dbus.MakeVariant(streams), "restore_token": dbus.MakeVariant(r.p.token)}), nil
}

func (r remoteIface) notify(name string, args ...any) *dbus.Error {
	r.p.mu.Lock()
	r.p.notifies = append(r.p.notifies, strings.TrimSpace(fmt.Sprintln(append([]any{name}, args...)...)))
	r.p.mu.Unlock()
	return nil
}

func (r remoteIface) NotifyPointerMotionAbsolute(s dbus.ObjectPath, o map[string]dbus.Variant, stream uint32, x, y float64) *dbus.Error {
	return r.notify("motion", stream, x, y)
}
func (r remoteIface) NotifyPointerButton(s dbus.ObjectPath, o map[string]dbus.Variant, b int32, st uint32) *dbus.Error {
	return r.notify("button", b, st)
}
func (r remoteIface) NotifyKeyboardKeysym(s dbus.ObjectPath, o map[string]dbus.Variant, sym int32, st uint32) *dbus.Error {
	return r.notify("keysym", sym, st)
}

type screencastIface struct{ p *fakePortal }

func (c screencastIface) SelectSources(sender dbus.Sender, session dbus.ObjectPath, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	remoteIface{c.p}.log("SelectSources")
	return c.p.respond(sender, tok(opts), map[string]dbus.Variant{}), nil
}

func startFakePortal(t *testing.T) *fakePortal {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	sock := filepath.Join(t.TempDir(), "bus")
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address", "--address=unix:path="+sock)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addr, _ := bufio.NewReader(out).ReadString('\n')
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))
	busMu.Lock()
	if bus != nil {
		bus.Close()
		bus = nil
	}
	busMu.Unlock()
	conn, err := dbus.Connect(strings.TrimSpace(addr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	p := &fakePortal{conn: conn, dir: t.TempDir(), token: "token-2"}
	conn.Export(screenshotIface{p}, portalPath, "org.freedesktop.portal.Screenshot")
	conn.Export(remoteIface{p}, portalPath, "org.freedesktop.portal.RemoteDesktop")
	conn.Export(screencastIface{p}, portalPath, "org.freedesktop.portal.ScreenCast")
	if reply, err := conn.RequestName(portalDest, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own portal name: %v %v", reply, err)
	}
	return p
}

func TestScreenshotThroughPortal(t *testing.T) {
	p := startFakePortal(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	img, err := Screenshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 64 {
		t.Fatalf("bounds %v", img.Bounds())
	}
	if r, _, _, _ := img.At(10, 10).RGBA(); r>>8 != 255 {
		t.Fatal("pixel lost")
	}
	if _, err := os.Stat(filepath.Join(p.dir, "shot.png")); !os.IsNotExist(err) {
		t.Fatal("portal file not removed")
	}
	if w, h, err := ScreenSize(ctx); err != nil || w != 64 || h != 48 {
		t.Fatalf("size %dx%d %v", w, h, err)
	}
	crop := ToRGBA(img, image.Rect(8, 8, 12, 12))
	if crop.Bounds().Dx() != 4 || crop.RGBAAt(2, 2).R != 255 {
		t.Fatalf("crop %v %v", crop.Bounds(), crop.RGBAAt(2, 2))
	}
}

func TestRemoteDesktopNeedsAndRotatesToken(t *testing.T) {
	p := startFakePortal(t)
	SetStateDir(t.TempDir())
	defer SetStateDir("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := StartRemoteDesktop(ctx, true); !errors.Is(err, ErrNoToken) {
		t.Fatalf("without token: %v", err)
	}
	os.WriteFile(TokenPath(), []byte("token-1\n"), 0o600)
	rd, err := StartRemoteDesktop(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.calls, ",") != "CreateSession,SelectDevices,SelectSources,Start" || p.gotToken != "token-1" {
		t.Fatalf("calls %v token %q", p.calls, p.gotToken)
	}
	if data, _ := os.ReadFile(TokenPath()); strings.TrimSpace(string(data)) != "token-2" {
		t.Fatalf("token not rotated: %q", data)
	}
	if rd.stream != 42 || rd.sw != 32 || rd.sh != 24 {
		t.Fatalf("stream %+v", rd)
	}
	// A 64x48 screenshot maps onto the 32x24 logical stream.
	rd.Move(20, 10, 64, 48)
	rd.Button(BtnLeft, true)
	rd.Keysym(0x01004e16, true)
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	got := strings.Join(p.notifies, "|")
	p.mu.Unlock()
	if got != "motion 42 10 5|button 272 1|keysym 16797206 1" {
		t.Fatalf("notifies = %q", got)
	}
}
