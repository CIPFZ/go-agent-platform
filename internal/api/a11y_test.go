package api

import (
	"context"
	"testing"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/a11y"
	"github.com/CIPFZ/go-agent-platform/internal/actions/window"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
)

func a11yEnv(t *testing.T, collect func(ctx context.Context, r a11y.Request) (*a11y.Result, error)) (*testEnv, *[]a11y.Request) {
	var seen []a11y.Request
	fw := &fakeWindows{list: []window.Window{
		{ID: "0x1", Title: "Back", PID: 1, Visible: true},
		{ID: "0x2", Title: "Front", PID: 2, Visible: true, Active: true},
	}}
	e := newEnv(t, func(d *Deps) {
		d.Window = fw
		d.A11yAvailable = func() bool { return true }
		d.ScreenOrigin = func(context.Context) (int, int, error) { return 10, 20, nil }
		d.A11yCollect = func(ctx context.Context, r a11y.Request) (*a11y.Result, error) {
			seen = append(seen, r)
			return collect(ctx, r)
		}
	})
	return e, &seen
}

func TestA11yTreeDefaultsToForegroundWindow(t *testing.T) {
	e, seen := a11yEnv(t, func(_ context.Context, r a11y.Request) (*a11y.Result, error) {
		return &a11y.Result{Window: r.Target, Root: &a11y.Node{Role: "frame", Name: r.Target.Title, State: []string{}, Children: []*a11y.Node{}}, Nodes: 1}, nil
	})
	var res a11y.Result
	e.call("POST", "/v1/a11y/tree", `{}`).into(t, &res)
	r := (*seen)[0]
	if r.Target.ID != "0x2" || r.MaxDepth != 10 || r.MaxNodes != 5000 || r.OriginX != 10 || r.OriginY != 20 || res.Root.Name != "Front" {
		t.Fatalf("request %+v result %+v", r, res)
	}
	e.call("POST", "/v1/a11y/tree", `{"window_id":"0x1","max_depth":3,"max_nodes":7}`).into(t, &res)
	if r := (*seen)[1]; r.Target.Title != "Back" || r.MaxDepth != 3 || r.MaxNodes != 7 {
		t.Fatalf("explicit request %+v", r)
	}
	e.call("POST", "/v1/a11y/tree", `{"window_id":"0x9"}`).code(t, protocol.CodeNotFound)
	e.call("POST", "/v1/a11y/tree", `{"max_depth":0}`).code(t, protocol.CodeInvalidRequest)
	e.call("POST", "/v1/a11y/tree", `{"max_nodes":50001}`).code(t, protocol.CodeInvalidRequest)
	var caps protocol.CapabilitiesResult
	e.call("GET", "/v1/capabilities", nil).into(t, &caps)
	found := false
	for _, a := range caps.Actions {
		found = found || a == "a11y"
	}
	if !found {
		t.Fatalf("actions = %v", caps.Actions)
	}
}

func TestA11yTimeoutIsRetryable(t *testing.T) {
	e, _ := a11yEnv(t, func(ctx context.Context, _ a11y.Request) (*a11y.Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	start := time.Now()
	r := e.call("POST", "/v1/a11y/tree", `{}`, protocol.HeaderTimeoutMS, "200")
	r.code(t, protocol.CodeTimeout)
	if !r.Error.Retryable || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout = %+v after %s", r.Error, time.Since(start))
	}
}
