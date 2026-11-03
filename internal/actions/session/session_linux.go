//go:build linux

package session

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/CIPFZ/go-agent-platform/internal/platform/x11"
)

// dbusTimeout bounds each login1 query so probe never hangs on D-Bus.
const dbusTimeout = time.Second

func detect() State {
	st := State{Type: "console"}
	if _, err := x11.Shared(); err == nil {
		st.DisplayReady = true
	}
	st.Locked = lockedHint()
	if st.Locked {
		st.DisplayReady = false
	}
	return st
}

// Linux coordinates are always physical pixels; there is no DPI
// virtualisation to opt out of.
func initDPIAwareness() string { return "not_applicable" }

var (
	busMu sync.Mutex
	bus   *dbus.Conn
)

func systemBus() (*dbus.Conn, error) {
	busMu.Lock()
	defer busMu.Unlock()
	if bus != nil && bus.Connected() {
		return bus, nil
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	bus = conn
	return conn, nil
}

const (
	login1Dest = "org.freedesktop.login1"
	login1Path = dbus.ObjectPath("/org/freedesktop/login1")
)

// lockedHint reads LockedHint of the user's graphical session through
// org.freedesktop.login1 (spec section 10). vm-agent runs as a systemd user
// service, which is outside any login session, so the session is found via
// the user's Display property, then $XDG_SESSION_ID, then our own pid. When
// logind is unreachable the session is reported unlocked.
func lockedHint() bool {
	conn, err := systemBus()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbusTimeout)
	defer cancel()
	path, ok := displaySession(ctx, conn)
	if !ok {
		return false
	}
	v, err := property(ctx, conn.Object(login1Dest, path), "org.freedesktop.login1.Session", "LockedHint")
	if err != nil {
		return false
	}
	locked, _ := v.Value().(bool)
	return locked
}

func displaySession(ctx context.Context, conn *dbus.Conn) (dbus.ObjectPath, bool) {
	manager := conn.Object(login1Dest, login1Path)
	var userPath dbus.ObjectPath
	if err := manager.CallWithContext(ctx, "org.freedesktop.login1.Manager.GetUser", 0, uint32(os.Getuid())).Store(&userPath); err == nil {
		if v, err := property(ctx, conn.Object(login1Dest, userPath), "org.freedesktop.login1.User", "Display"); err == nil {
			if pair, ok := v.Value().([]any); ok && len(pair) == 2 {
				if p, ok := pair[1].(dbus.ObjectPath); ok && p != "/" && p != "" {
					return p, true
				}
			}
		}
	}
	var path dbus.ObjectPath
	if id := os.Getenv("XDG_SESSION_ID"); id != "" {
		if err := manager.CallWithContext(ctx, "org.freedesktop.login1.Manager.GetSession", 0, id).Store(&path); err == nil {
			return path, true
		}
	}
	if err := manager.CallWithContext(ctx, "org.freedesktop.login1.Manager.GetSessionByPID", 0, uint32(os.Getpid())).Store(&path); err == nil {
		return path, true
	}
	return "", false
}

func property(ctx context.Context, obj dbus.BusObject, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(&v)
	return v, err
}
