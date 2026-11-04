package api

import (
	"strconv"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/a11y"
	"github.com/CIPFZ/go-agent-platform/internal/actions/window"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

const timeoutA11y = 15 * time.Second // spec 6.9 and section 7

func (a *API) a11yRoutes() []*server.Route {
	return []*server.Route{
		{Method: "POST", Path: "/v1/a11y/tree", Action: "a11y.tree", Class: server.Retryable, Handler: a.a11yTree},
	}
}

// a11yTree returns the accessibility tree of window_id, or of the
// foreground window when it is omitted (as agreed for phase 3).
func (a *API) a11yTree(c *server.Call) (any, error) {
	var req protocol.A11yTreeRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	r := a11y.Request{MaxDepth: a11y.DefaultMaxDepth, MaxNodes: a11y.DefaultMaxNodes}
	if req.MaxDepth != nil {
		r.MaxDepth = *req.MaxDepth
	}
	if req.MaxNodes != nil {
		r.MaxNodes = *req.MaxNodes
	}
	if r.MaxDepth < 1 || r.MaxDepth > a11y.MaxDepthCap || r.MaxNodes < 1 || r.MaxNodes > a11y.MaxNodesCap {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "max_depth must be 1..%d and max_nodes 1..%d", a11y.MaxDepthCap, a11y.MaxNodesCap)
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	if !a.a11yAvailable() {
		return nil, protocol.Unsupported("no accessibility provider is available (UI Automation on Windows, the AT-SPI bus on Linux)")
	}
	ctx := c.Context(timeoutA11y)
	var w *window.Window
	var err error
	if req.WindowID != "" {
		var found window.Window
		found, err = a.window.Resolve(ctx, window.Selector{ID: req.WindowID})
		w = &found
	} else {
		w, err = a.window.Active(ctx)
		if err == nil && w == nil {
			err = protocol.Errorf(protocol.CodeNotFound, "no window is in the foreground")
		}
	}
	if err != nil {
		return nil, a.displayError(err)
	}
	r.Target = a11y.Target{ID: w.ID, Title: w.Title, PID: w.PID}
	if r.OriginX, r.OriginY, err = a.screenOrigin(ctx); err != nil {
		return nil, a.displayError(err)
	}
	c.Param("window_id", w.ID)
	res, err := a.a11yCollect(ctx, r)
	if err != nil {
		return nil, err
	}
	c.Param("nodes", strconv.Itoa(res.Nodes))
	return res, nil
}
