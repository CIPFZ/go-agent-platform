package remote

import (
	"sync"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// VirtualDisplay adds a monitor that exists only in software. While a
// laptop's lid is closed no display is active, the desktop compositor idles
// and only GDI capture returns frames; a virtual monitor makes it compose
// again (docs/remote-desktop-design.md 5.11).
type VirtualDisplay interface {
	// Installed reports whether the driver is present.
	Installed() bool
	// Active reports whether some display is being composed for.
	Active() bool
	// Plugged is the number of virtual monitors currently connected.
	Plugged() int
	// Plug adds a monitor of the given size and scale and returns once the
	// desktop uses it.
	Plug(size protocol.Rect, scale float64) error
	// Unplug removes one monitor.
	Unplug() error
	// RealDisplay reports whether a display other than the virtual ones is
	// part of the desktop.
	RealDisplay() bool
}

// vdisplayState tracks the monitor a session added, so it is removed with
// the session and never one that something else added.
type vdisplayState struct {
	mu      sync.Mutex
	plugged bool
	// changed is when a monitor was last added or removed; the display
	// state is left to settle before it is acted on again.
	changed time.Time
}

// virtualDisplaySettle keeps tendDisplay from reacting to the transient
// state right after a monitor was added or removed.
const virtualDisplaySettle = 5 * time.Second

// virtualDisplayWait is how long the compositor is given to start presenting
// to a monitor that was just added.
const virtualDisplayWait = 3 * time.Second

// ensureDisplay adds a virtual monitor when a session starts while no
// display is active. It reports what it did for the audit log; failing to
// add one is not an error for the session, which then captures with GDI.
func (m *Manager) ensureDisplay(size protocol.Rect, scale float64) string {
	vd := m.o.VirtualDisplay
	if vd == nil || !vd.Installed() {
		return ""
	}
	m.vd.mu.Lock()
	defer m.vd.mu.Unlock()
	if vd.Active() {
		return ""
	}
	if vd.Plugged() > 0 {
		// One is connected and still nothing is composed: adding another
		// would not help.
		return "already_plugged"
	}
	if err := vd.Plug(size, scale); err != nil {
		logf("virtual display: %v", err)
		return "failed"
	}
	m.vd.plugged = true
	m.vd.changed = time.Now()
	writeMarker(m.o.StateDir)
	for deadline := time.Now().Add(virtualDisplayWait); time.Now().Before(deadline) && !vd.Active(); time.Sleep(50 * time.Millisecond) {
	}
	return "plugged"
}

// releaseDisplay removes the monitor a session added.
func (m *Manager) releaseDisplay() {
	vd := m.o.VirtualDisplay
	if vd == nil {
		return
	}
	m.vd.mu.Lock()
	defer m.vd.mu.Unlock()
	if !m.vd.plugged {
		return
	}
	m.vd.plugged = false
	m.vd.changed = time.Now()
	if err := vd.Unplug(); err != nil {
		logf("virtual display: unplug: %v", err)
		return
	}
	removeMarker(m.o.StateDir)
}

// tendDisplay follows the lid during a session: when a real display comes
// back (the lid is opened) the virtual monitor is removed so the desktop is
// on the panel again, and when the last display goes away (the lid is
// closed) one is added.
func (m *Manager) tendDisplay(size protocol.Rect, scale float64) {
	vd := m.o.VirtualDisplay
	if vd == nil || !vd.Installed() {
		return
	}
	m.vd.mu.Lock()
	plugged, settled := m.vd.plugged, time.Since(m.vd.changed) > virtualDisplaySettle
	m.vd.mu.Unlock()
	switch {
	case !settled:
	case plugged && vd.RealDisplay():
		m.releaseDisplay()
	case !plugged && !vd.Active():
		m.ensureDisplay(size, scale)
	}
}

// rememberDisplay notes the primary display's size and scale while a real
// display is the one being composed for.
func (m *Manager) rememberDisplay(d screen.Display) {
	vd := m.o.VirtualDisplay
	if vd == nil || m.o.StateDir == "" || d.Bounds.W <= 0 || d.Bounds.H <= 0 || d.Scale <= 0 {
		return
	}
	if vd.Plugged() == 0 && vd.Active() {
		saveDisplayRecord(m.o.StateDir, displayRecord{W: d.Bounds.W, H: d.Bounds.H, Scale: d.Scale})
	}
}

// virtualGeometry is the size and scale a virtual monitor gets: the real
// display's when it is known, else what the desktop reports now.
func (m *Manager) virtualGeometry(d screen.Display) (protocol.Rect, float64) {
	if r, ok := loadDisplayRecord(m.o.StateDir); ok {
		return protocol.Rect{W: r.W, H: r.H}, r.Scale
	}
	return d.Bounds, d.Scale
}

// cursorEmbedded reports whether the pointer is part of the captured
// picture. The virtual monitor has no hardware cursor, so Windows draws the
// pointer into its image; the page must then not draw one of its own.
func (m *Manager) cursorEmbedded() bool {
	vd := m.o.VirtualDisplay
	return vd != nil && vd.Plugged() > 0 && !vd.RealDisplay()
}

// reclaimDisplay removes virtual monitors left connected by a run that ended
// without closing its session. Whether one is left is recorded in a marker
// file, so a monitor the user added on purpose is not touched.
func (m *Manager) reclaimDisplay() {
	vd := m.o.VirtualDisplay
	if vd == nil || m.o.StateDir == "" || !vd.Installed() {
		return
	}
	if !markerExists(m.o.StateDir) {
		return
	}
	for i := 0; i < 4 && vd.Plugged() > 0; i++ {
		if err := vd.Unplug(); err != nil {
			logf("virtual display: reclaim: %v", err)
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	removeMarker(m.o.StateDir)
}
