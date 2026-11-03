//go:build linux

package integration

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/CIPFZ/go-agent-platform/pkg/client"
)

// fakeLogin1 serves the parts of org.freedesktop.login1 vm-agent reads:
// Manager.GetUser, User.Display and Session.LockedHint.
type fakeLogin1 struct{ locked atomic.Bool }

const (
	userPath    = dbus.ObjectPath("/org/freedesktop/login1/user/_test")
	sessionPath = dbus.ObjectPath("/org/freedesktop/login1/session/c1")
)

type manager struct{}

func (manager) GetUser(uint32) (dbus.ObjectPath, *dbus.Error) { return userPath, nil }

type userProps struct{}

func (userProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface == "org.freedesktop.login1.User" && name == "Display" {
		return dbus.MakeVariant(struct {
			ID   string
			Path dbus.ObjectPath
		}{"c1", sessionPath}), nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(os.ErrNotExist)
}

type sessionProps struct{ f *fakeLogin1 }

func (s sessionProps) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	if iface == "org.freedesktop.login1.Session" && name == "LockedHint" {
		return dbus.MakeVariant(s.f.locked.Load()), nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(os.ErrNotExist)
}

// startFakeLogin1 runs a private dbus-daemon and registers the fake service
// on it, returning the bus address to pass as DBUS_SYSTEM_BUS_ADDRESS.
func startFakeLogin1(t *testing.T) (*fakeLogin1, string) {
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
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(line)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f := &fakeLogin1{}
	conn.Export(manager{}, "/org/freedesktop/login1", "org.freedesktop.login1.Manager")
	conn.Export(userProps{}, userPath, "org.freedesktop.DBus.Properties")
	conn.Export(sessionProps{f}, sessionPath, "org.freedesktop.DBus.Properties")
	if reply, err := conn.RequestName("org.freedesktop.login1", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}
	return f, addr
}

// Spec 13.1 acceptance 5: while the session is locked probe says so and
// input is refused with no_display; unlocking restores both.
func TestLockedSessionReportedAndInputRefused(t *testing.T) {
	requireX(t)
	login1, addr := startFakeLogin1(t)
	a := startAgent(t, withEnv("DBUS_SYSTEM_BUS_ADDRESS="+addr))
	p, err := a.client.Probe(ctx)
	if err != nil || p.SessionLocked || !p.Display.Ready {
		t.Fatalf("unlocked probe = %+v, %v", p, err)
	}
	login1.locked.Store(true)
	p, _ = a.client.Probe(ctx)
	if !p.SessionLocked || p.Display.Ready {
		t.Fatalf("locked probe = %+v", p)
	}
	caps, _ := a.client.Capabilities(ctx)
	for _, act := range caps.Actions {
		if act == "input" || act == "screen" || act == "window" {
			t.Fatalf("capabilities advertise %s while locked: %v", act, caps.Actions)
		}
	}
	for name, err := range map[string]error{
		"click":   a.client.MouseClick(ctx, &client.MouseClickRequest{}),
		"type":    a.client.KeyType(ctx, &client.KeyTypeRequest{Text: "x"}),
		"capture": func() error { _, err := a.client.ScreenCapture(ctx, &client.CaptureRequest{}); return err }(),
	} {
		if e := apiErr(t, err); e.Code != "no_display" || !e.Retryable || e.HTTPStatus != 503 {
			t.Errorf("%s while locked = %+v", name, e)
		}
	}
	login1.locked.Store(false)
	time.Sleep(50 * time.Millisecond)
	if err := a.client.MouseMove(ctx, &client.MouseMoveRequest{X: 5, Y: 5}); err != nil {
		t.Fatalf("input after unlock: %v", err)
	}
}
