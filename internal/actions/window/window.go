// Package window implements /v1/window: enumeration, matching, activation,
// closing and resizing of top-level windows.
package window

import "github.com/CIPFZ/go-agent-platform/internal/protocol"

// Window is one top-level window. Rect and ClientRect are nil while the window
// is minimized.
type Window struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Class      string         `json:"class"`
	PID        int            `json:"pid"`
	Rect       *protocol.Rect `json:"rect"`
	ClientRect *protocol.Rect `json:"client_rect"`
	Active     bool           `json:"active"`
	Minimized  bool           `json:"minimized"`

	// Shell marks desktop shell windows and Tool marks tool windows; both are
	// never touched by close_all (spec section 6.4).
	Shell bool `json:"-"`
	Tool  bool `json:"-"`
}

// State is a show state for resize.
type State string

const (
	StateMaximize State = "maximize"
	StateMinimize State = "minimize"
	StateRestore  State = "restore"
)

// Backend is the platform window manager interface.
type Backend interface {
	// List returns windows in Z order, topmost first.
	List(visibleOnly bool) ([]Window, error)
	Activate(id string) error
	Close(id string) error
	SetState(id string, state State) error
	SetRect(id string, r protocol.Rect) error
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }
