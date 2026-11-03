// Package window implements /v1/window: enumeration, matching, activation,
// closing and resizing of top-level windows (spec section 6.4).
package window

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/async"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

// Window is one top-level window. Rect (whole window) and ClientRect are in
// physical virtual-desktop pixels and nil while the window is minimized.
type Window struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Class      string         `json:"class"`
	PID        int            `json:"pid"`
	Rect       *protocol.Rect `json:"rect"`
	ClientRect *protocol.Rect `json:"client_rect"`
	Active     bool           `json:"active"`
	Minimized  bool           `json:"minimized"`

	// Visible is false for hidden (unmapped, cloaked) windows, which only
	// appear with visible_only=false.
	Visible bool `json:"-"`
	// Shell marks desktop shell windows and Tool marks tool windows; both are
	// never touched by close_all (spec section 6.4).
	Shell bool `json:"-"`
	Tool  bool `json:"-"`
	// Maximized is used by SetRect to restore first.
	Maximized bool `json:"-"`
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
	// List returns top-level windows in Z order, topmost first. With
	// visibleOnly false hidden windows are included.
	List(visibleOnly bool) ([]Window, error)
	// Activate brings id to the foreground, restoring it if minimized.
	Activate(id string) error
	// Close asks id to close (WM_CLOSE, _NET_CLOSE_WINDOW); never kills.
	Close(id string) error
	SetState(id string, state State) error
	// SetRect moves and resizes id so its whole-window rectangle is r.
	SetRect(id string, r protocol.Rect) error
}

// NewBackend returns the backend for the current platform and session.
func NewBackend() Backend { return newBackend() }

// Service wraps a Backend. Every backend call runs on an abandonable
// goroutine because enumeration can block on hung applications (spec 7).
type Service struct {
	backend Backend
}

// NewService returns a Service over b.
func NewService(b Backend) *Service { return &Service{backend: b} }

// List lists windows.
func (s *Service) List(ctx context.Context, visibleOnly bool) ([]Window, error) {
	return async.Run(ctx, func() ([]Window, error) { return s.backend.List(visibleOnly) })
}

// Active returns the foreground window, or nil if there is none.
func (s *Service) Active(ctx context.Context) (*Window, error) {
	windows, err := s.List(ctx, false)
	if err != nil {
		return nil, err
	}
	for i := range windows {
		if windows[i].Active {
			return &windows[i], nil
		}
	}
	return nil, nil
}

// Selector identifies one window: by id, or by title with match mode plus
// optional class and pid filters, resolved with pick (spec 6.4).
type Selector struct {
	ID    string  `json:"id"`
	Title *string `json:"title"`
	Match string  `json:"match"`
	Class string  `json:"class"`
	PID   int     `json:"pid"`
	Pick  string  `json:"pick"`
}

// Validate checks the selector fields.
func (sel *Selector) Validate() error {
	if sel.ID == "" && sel.Title == nil && sel.Class == "" && sel.PID == 0 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "give id, or title with optional class and pid")
	}
	if sel.ID != "" && (sel.Title != nil || sel.Class != "" || sel.PID != 0) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "id cannot be combined with title, class or pid")
	}
	switch sel.Match {
	case "", "exact", "contains", "regex":
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "match must be exact, contains or regex")
	}
	if sel.Match != "" && sel.Title == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "match requires title")
	}
	if sel.Match == "regex" {
		if _, err := regexp.Compile(*sel.Title); err != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "invalid title regex: %v", err)
		}
	}
	switch sel.Pick {
	case "", "error", "first", "topmost":
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "pick must be first, topmost or error")
	}
	return nil
}

// Matches filters windows (in List order) by the selector criteria.
func (sel *Selector) Matches(windows []Window) []Window {
	var re *regexp.Regexp
	if sel.Match == "regex" {
		re = regexp.MustCompile(*sel.Title)
	}
	var out []Window
	for _, w := range windows {
		if sel.ID != "" {
			if sameID(w.ID, sel.ID) {
				out = append(out, w)
			}
			continue
		}
		if sel.Title != nil {
			switch sel.Match {
			case "contains":
				if !strings.Contains(w.Title, *sel.Title) {
					continue
				}
			case "regex":
				if !re.MatchString(w.Title) {
					continue
				}
			default:
				if w.Title != *sel.Title {
					continue
				}
			}
		}
		if sel.Class != "" && w.Class != sel.Class {
			continue
		}
		if sel.PID != 0 && w.PID != sel.PID {
			continue
		}
		out = append(out, w)
	}
	return out
}

// sameID compares window ids numerically so "0x1A" and "0x1a" agree.
func sameID(a, b string) bool {
	x, errA := ParseID(a)
	y, errB := ParseID(b)
	return errA == nil && errB == nil && x == y
}

// FormatID renders a native handle as a window id.
func FormatID(v uint64) string { return "0x" + strconv.FormatUint(v, 16) }

// ParseID parses a window id produced by FormatID (decimal is accepted).
func ParseID(id string) (uint64, error) {
	s := strings.TrimSpace(id)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

// Resolve finds the single window selected by sel. The list is in Z order,
// so "first" (first in list order) and "topmost" pick the same window.
func (s *Service) Resolve(ctx context.Context, sel Selector) (Window, error) {
	if err := sel.Validate(); err != nil {
		return Window{}, err
	}
	windows, err := s.List(ctx, sel.ID == "")
	if err != nil {
		return Window{}, err
	}
	matches := sel.Matches(windows)
	switch {
	case len(matches) == 0:
		return Window{}, protocol.Errorf(protocol.CodeNotFound, "no window matches the selector")
	case len(matches) == 1 || sel.Pick == "first" || sel.Pick == "topmost":
		return matches[0], nil
	}
	return Window{}, protocol.Errorf(protocol.CodeAmbiguous, "%d windows match the selector", len(matches)).WithDetail("candidates", matches)
}

// refresh re-reads one window after an operation.
func (s *Service) refresh(ctx context.Context, id string) (Window, error) {
	windows, err := s.List(ctx, false)
	if err != nil {
		return Window{}, err
	}
	for _, w := range windows {
		if sameID(w.ID, id) {
			return w, nil
		}
	}
	return Window{}, protocol.Errorf(protocol.CodeNotFound, "window %s no longer exists", id)
}

// Activate brings the selected window to the foreground and returns it.
func (s *Service) Activate(ctx context.Context, sel Selector) (Window, error) {
	w, err := s.Resolve(ctx, sel)
	if err != nil {
		return Window{}, err
	}
	if _, err := async.Run(ctx, func() (struct{}, error) { return struct{}{}, s.backend.Activate(w.ID) }); err != nil {
		return Window{}, err
	}
	return s.refresh(ctx, w.ID)
}

// Close sends a close request to the selected window and returns it as it
// was before the request.
func (s *Service) Close(ctx context.Context, sel Selector) (Window, error) {
	w, err := s.Resolve(ctx, sel)
	if err != nil {
		return Window{}, err
	}
	_, err = async.Run(ctx, func() (struct{}, error) { return struct{}{}, s.backend.Close(w.ID) })
	return w, err
}

// SetState maximizes, minimizes or restores id.
func (s *Service) SetState(ctx context.Context, id string, state State) (Window, error) {
	if _, err := s.refresh(ctx, id); err != nil {
		return Window{}, err
	}
	if _, err := async.Run(ctx, func() (struct{}, error) { return struct{}{}, s.backend.SetState(id, state) }); err != nil {
		return Window{}, err
	}
	return s.settle(ctx, id, func(w Window) bool {
		switch state {
		case StateMinimize:
			return w.Minimized
		case StateMaximize:
			return w.Maximized && !w.Minimized
		default:
			return !w.Minimized && !w.Maximized
		}
	})
}

// SetRect moves and resizes id, restoring it first when maximized or
// minimized (spec 6.4).
func (s *Service) SetRect(ctx context.Context, id string, r protocol.Rect) (Window, error) {
	w, err := s.refresh(ctx, id)
	if err != nil {
		return Window{}, err
	}
	if w.Minimized || w.Maximized {
		if _, err := s.SetState(ctx, id, StateRestore); err != nil {
			return Window{}, err
		}
	}
	if _, err := async.Run(ctx, func() (struct{}, error) { return struct{}{}, s.backend.SetRect(id, r) }); err != nil {
		return Window{}, err
	}
	return s.settle(ctx, id, func(w Window) bool { return w.Rect != nil && *w.Rect == r })
}

// settle polls briefly until done reports the asynchronous window-manager
// change as applied, returning the latest state either way.
func (s *Service) settle(ctx context.Context, id string, done func(Window) bool) (Window, error) {
	deadline := time.Now().Add(time.Second)
	for {
		w, err := s.refresh(ctx, id)
		if err != nil || done(w) || time.Now().After(deadline) {
			return w, err
		}
		select {
		case <-ctx.Done():
			return w, nil
		case <-time.After(25 * time.Millisecond):
		}
	}
}
