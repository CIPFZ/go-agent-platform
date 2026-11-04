package window

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

type fakeWM struct {
	mu        sync.Mutex
	windows   []Window
	activated []string
	closed    []string
	states    []State
	rects     []protocol.Rect
}

func (f *fakeWM) List(visibleOnly bool) ([]Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Window
	for _, w := range f.windows {
		if visibleOnly && !w.Visible {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

func (f *fakeWM) Activate(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activated = append(f.activated, id)
	for i := range f.windows {
		f.windows[i].Active = sameID(f.windows[i].ID, id)
		if f.windows[i].Active {
			f.windows[i].Minimized = false
		}
	}
	return nil
}

func (f *fakeWM) Close(id string) error { f.closed = append(f.closed, id); return nil }

func (f *fakeWM) SetState(id string, s State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, s)
	for i := range f.windows {
		if sameID(f.windows[i].ID, id) {
			f.windows[i].Minimized = s == StateMinimize
			f.windows[i].Maximized = s == StateMaximize
		}
	}
	return nil
}

func (f *fakeWM) SetRect(id string, r protocol.Rect) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rects = append(f.rects, r)
	for i := range f.windows {
		if sameID(f.windows[i].ID, id) {
			rr := r
			f.windows[i].Rect = &rr
		}
	}
	return nil
}

func sample() *fakeWM {
	return &fakeWM{windows: []Window{
		{ID: "0x10", Title: "Untitled - Notepad", Class: "Notepad", PID: 100, Visible: true},
		{ID: "0x20", Title: "notes.txt - Notepad", Class: "Notepad", PID: 200, Visible: true, Minimized: true},
		{ID: "0x30", Title: "Calculator", Class: "ApplicationFrameWindow", PID: 300, Visible: true, Active: true},
		{ID: "0x40", Title: "Hidden helper", Class: "Helper", PID: 100, Visible: false},
	}}
}

func str(s string) *string { return &s }

func code(err error) protocol.Code {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return ""
}

func TestResolveSelectors(t *testing.T) {
	s := NewService(sample())
	ctx := context.Background()
	cases := []struct {
		name string
		sel  Selector
		id   string
		err  protocol.Code
	}{
		{"by id", Selector{ID: "0x20"}, "0x20", ""},
		{"id case and decimal", Selector{ID: "32"}, "0x20", ""},
		{"hidden by id", Selector{ID: "0x40"}, "0x40", ""},
		{"exact title", Selector{Title: str("Calculator")}, "0x30", ""},
		{"exact is default", Selector{Title: str("Notepad")}, "", protocol.CodeNotFound},
		{"contains ambiguous", Selector{Title: str("Notepad"), Match: "contains"}, "", protocol.CodeAmbiguous},
		{"contains narrowed by pid", Selector{Title: str("Notepad"), Match: "contains", PID: 200}, "0x20", ""},
		{"regex with class", Selector{Title: str(`^notes\.`), Match: "regex", Class: "Notepad"}, "0x20", ""},
		{"pick first", Selector{Title: str("Notepad"), Match: "contains", Pick: "first"}, "0x10", ""},
		{"pick topmost", Selector{Title: str("Notepad"), Match: "contains", Pick: "topmost"}, "0x10", ""},
		{"hidden excluded by title", Selector{Title: str("Hidden helper")}, "", protocol.CodeNotFound},
		{"class alone", Selector{Class: "ApplicationFrameWindow"}, "0x30", ""},
		{"empty selector", Selector{}, "", protocol.CodeInvalidRequest},
		{"bad match", Selector{Title: str("x"), Match: "glob"}, "", protocol.CodeInvalidRequest},
		{"bad regex", Selector{Title: str("("), Match: "regex"}, "", protocol.CodeInvalidRequest},
		{"id with title", Selector{ID: "0x10", Title: str("x")}, "", protocol.CodeInvalidRequest},
		{"bad pick", Selector{Title: str("x"), Pick: "last"}, "", protocol.CodeInvalidRequest},
	}
	for _, tc := range cases {
		w, err := s.Resolve(ctx, tc.sel)
		if tc.err != "" {
			if code(err) != tc.err {
				t.Errorf("%s: err = %v, want %s", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || w.ID != tc.id {
			t.Errorf("%s: got %q, %v; want %s", tc.name, w.ID, err, tc.id)
		}
	}
}

func TestAmbiguousListsCandidates(t *testing.T) {
	_, err := NewService(sample()).Resolve(context.Background(), Selector{Title: str("Notepad"), Match: "contains"})
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeAmbiguous || perr.Code.HTTPStatus() != 409 {
		t.Fatalf("err = %v", err)
	}
	if c, ok := perr.Details["candidates"].([]Window); !ok || len(c) != 2 {
		t.Fatalf("candidates = %#v", perr.Details["candidates"])
	}
}

func TestActivateReturnsRefreshedWindow(t *testing.T) {
	f := sample()
	w, err := NewService(f).Activate(context.Background(), Selector{ID: "0x20"})
	if err != nil || !w.Active || w.Minimized || len(f.activated) != 1 {
		t.Fatalf("w = %+v, err = %v, activated = %v", w, err, f.activated)
	}
}

func TestCloseSendsRequest(t *testing.T) {
	f := sample()
	w, err := NewService(f).Close(context.Background(), Selector{Title: str("Calculator")})
	if err != nil || w.ID != "0x30" || len(f.closed) != 1 || f.closed[0] != "0x30" {
		t.Fatalf("w = %+v err = %v closed = %v", w, err, f.closed)
	}
}

func TestSetRectRestoresFirst(t *testing.T) {
	f := sample()
	s := NewService(f)
	r := protocol.Rect{X: 10, Y: 20, W: 300, H: 200}
	w, err := s.SetRect(context.Background(), "0x20", r)
	if err != nil || len(f.states) != 1 || f.states[0] != StateRestore || w.Rect == nil || *w.Rect != r {
		t.Fatalf("w = %+v err = %v states = %v", w, err, f.states)
	}
	if _, err := s.SetRect(context.Background(), "0x99", r); code(err) != protocol.CodeNotFound {
		t.Fatalf("missing window err = %v", err)
	}
}

func TestIDFormat(t *testing.T) {
	if FormatID(0x1a2b) != "0x1a2b" {
		t.Fatal(FormatID(0x1a2b))
	}
	for _, in := range []string{"0x1A2B", "0X1a2b", "6699"} {
		if v, err := ParseID(in); err != nil || v != 0x1a2b {
			t.Errorf("ParseID(%q) = %x, %v", in, v, err)
		}
	}
	if _, err := ParseID("zz"); err == nil {
		t.Error("bad id parsed")
	}
}

func TestCloseCandidatesExclusions(t *testing.T) {
	ws := []Window{
		{ID: "0x1", Title: "Desktop", PID: 10, Visible: true, Shell: true},
		{ID: "0x2", Title: "Tool palette", PID: 11, Visible: true, Tool: true},
		{ID: "0x3", Title: "hidden", PID: 12, Visible: false},
		{ID: "0x4", Title: "agent window", PID: 99, Visible: true},
		{ID: "0x5", Title: "Keep me - Editor", PID: 13, Visible: true},
		{ID: "0x6", Title: "excluded pid", PID: 14, Visible: true},
		{ID: "0x7", Title: "Close me", PID: 15, Visible: true, Minimized: true},
	}
	got := closeCandidates(ws, CloseAllOptions{ExcludeTitles: []string{"Keep me"}, ExcludePIDs: []int{14}, SelfPIDs: []int{99}})
	if len(got) != 1 || got[0].ID != "0x7" {
		t.Fatalf("candidates = %+v", got)
	}
}

type closingWM struct {
	fakeWM
	stubborn map[string]bool
}

func (c *closingWM) Close(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = append(c.closed, id)
	if c.stubborn[id] {
		return nil
	}
	for i, w := range c.windows {
		if w.ID == id {
			c.windows = append(c.windows[:i], c.windows[i+1:]...)
			break
		}
	}
	return nil
}

func TestCloseAllReportsClosedAndRemaining(t *testing.T) {
	wm := &closingWM{fakeWM: fakeWM{windows: []Window{
		{ID: "0x1", Title: "a", PID: 1, Visible: true},
		{ID: "0x2", Title: "b", PID: 2, Visible: true},
		{ID: "0x3", Title: "shell", PID: 3, Visible: true, Shell: true},
	}}, stubborn: map[string]bool{"0x2": true}}
	s := NewService(wm)
	start := time.Now()
	res, err := s.CloseAll(context.Background(), CloseAllOptions{Wait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Closed) != 1 || res.Closed[0].ID != "0x1" || len(res.Remaining) != 1 || res.Remaining[0].ID != "0x2" {
		t.Fatalf("result = %+v", res)
	}
	if took := time.Since(start); took < 300*time.Millisecond || took > 2*time.Second {
		t.Fatalf("waited %s", took)
	}
	for _, id := range wm.closed {
		if id == "0x3" {
			t.Fatal("shell window was asked to close")
		}
	}
	// The wait is cut short so the answer arrives before the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	start = time.Now()
	if _, err := s.CloseAll(ctx, CloseAllOptions{Wait: 10 * time.Second}); err != nil || time.Since(start) > 600*time.Millisecond {
		t.Fatalf("bounded wait: %v after %s", err, time.Since(start))
	}
}
