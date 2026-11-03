package api

import (
	"sync"
	"testing"

	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/actions/window"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

type fakeWindows struct {
	mu      sync.Mutex
	list    []window.Window
	closed  []string
	actives []string
}

func (f *fakeWindows) List(visibleOnly bool) ([]window.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []window.Window
	for _, w := range f.list {
		if !visibleOnly || w.Visible {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeWindows) Activate(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actives = append(f.actives, id)
	for i := range f.list {
		f.list[i].Active = f.list[i].ID == id
	}
	return nil
}

func (f *fakeWindows) Close(id string) error               { f.closed = append(f.closed, id); return nil }
func (f *fakeWindows) SetState(string, window.State) error { return nil }
func (f *fakeWindows) SetRect(string, protocol.Rect) error { return nil }

func windowEnv(t *testing.T) (*testEnv, *fakeWindows) {
	fw := &fakeWindows{list: []window.Window{
		{ID: "0x1", Title: "Editor", Class: "Code", PID: 7, Visible: true, Rect: &protocol.Rect{W: 10, H: 10}, ClientRect: &protocol.Rect{W: 8, H: 8}},
		{ID: "0x2", Title: "Editor", Class: "Code", PID: 8, Visible: true, Minimized: true},
		{ID: "0x3", Title: "Background", Class: "Svc", PID: 9, Visible: false},
	}}
	return newEnv(t, func(d *Deps) { d.Window = fw }), fw
}

func TestWindowListAndActive(t *testing.T) {
	e, _ := windowEnv(t)
	var res struct {
		Windows []map[string]any `json:"windows"`
	}
	e.call("GET", "/v1/window/list", nil).into(t, &res)
	if len(res.Windows) != 2 {
		t.Fatalf("visible list = %v", res.Windows)
	}
	minimized := res.Windows[1]
	if minimized["rect"] != nil || minimized["client_rect"] != nil || minimized["minimized"] != true {
		t.Fatalf("minimized window must have null rects: %v", minimized)
	}
	for _, key := range []string{"id", "title", "class", "pid", "rect", "client_rect", "active", "minimized"} {
		if _, ok := res.Windows[0][key]; !ok {
			t.Fatalf("missing field %s", key)
		}
	}
	e.call("GET", "/v1/window/list?visible_only=false", nil).into(t, &res)
	if len(res.Windows) != 3 {
		t.Fatalf("full list = %v", res.Windows)
	}
	e.call("GET", "/v1/window/list?visible_only=maybe", nil).code(t, protocol.CodeInvalidRequest)
	e.call("GET", "/v1/window/active", nil).code(t, protocol.CodeNotFound)
}

func TestWindowActivateAmbiguityAndPick(t *testing.T) {
	e, fw := windowEnv(t)
	r := e.call("POST", "/v1/window/activate", `{"title":"Editor"}`)
	r.code(t, protocol.CodeAmbiguous)
	if r.Status != 409 || len(r.Error.Details["candidates"].([]any)) != 2 {
		t.Fatalf("ambiguous = %d %+v", r.Status, r.Error.Details)
	}
	var w window.Window
	e.call("POST", "/v1/window/activate", `{"title":"Editor","pick":"topmost"}`).into(t, &w)
	if w.ID != "0x1" || !w.Active || len(fw.actives) != 1 {
		t.Fatalf("activated = %+v", w)
	}
	e.call("GET", "/v1/window/active", nil).into(t, &w)
	if w.ID != "0x1" {
		t.Fatalf("active = %+v", w)
	}
	e.call("POST", "/v1/window/activate", `{"title":"Nope"}`).code(t, protocol.CodeNotFound)
}

func TestWindowCloseAndGating(t *testing.T) {
	e, fw := windowEnv(t)
	var w window.Window
	e.call("POST", "/v1/window/close", `{"title":"Editor","pid":8}`).into(t, &w)
	if w.ID != "0x2" || len(fw.closed) != 1 {
		t.Fatalf("closed = %v", fw.closed)
	}
	e.setSession(session.State{})
	e.call("GET", "/v1/window/list", nil).code(t, protocol.CodeNoDisplay)
}
