package api

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/window"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

const timeoutWindow = timeoutScreen // spec section 7: window/* 5 s

func (a *API) windowRoutes() []*server.Route {
	return []*server.Route{
		{Method: "GET", Path: "/v1/window/list", Action: "window.list", Class: server.Retryable, Handler: a.windowList},
		{Method: "GET", Path: "/v1/window/active", Action: "window.active", Class: server.Retryable, Handler: a.windowActive},
		{Method: "POST", Path: "/v1/window/activate", Action: "window.activate", Class: server.NonRetryable, Handler: a.windowActivate},
		{Method: "POST", Path: "/v1/window/close", Action: "window.close", Class: server.NonRetryable, Handler: a.windowClose},
		{Method: "POST", Path: "/v1/window/resize", Action: "window.resize", Class: server.NonRetryable, Handler: a.windowResize},
		{Method: "POST", Path: "/v1/window/close_all", Action: "window.close_all", Class: server.NonRetryable, Handler: a.windowCloseAll},
	}
}

func (a *API) windowResize(c *server.Call) (any, error) {
	var req protocol.WindowResizeRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	if req.ID == "" {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "id is required")
	}
	if (req.Rect == nil) == (req.State == "") {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "give exactly one of rect or state")
	}
	if _, err := window.ParseID(req.ID); err != nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid window id %q", req.ID)
	}
	c.Param("id", req.ID)
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	ctx := c.Context(timeoutWindow)
	var w window.Window
	var err error
	if req.Rect != nil {
		if req.Rect.Empty() {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "rect width and height must be positive")
		}
		c.Param("rect", fmt.Sprintf("%d,%d,%d,%d", req.Rect.X, req.Rect.Y, req.Rect.W, req.Rect.H))
		w, err = a.window.SetRect(ctx, req.ID, *req.Rect)
	} else {
		state := window.State(req.State)
		switch state {
		case window.StateMaximize, window.StateMinimize, window.StateRestore:
		default:
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "state must be maximize, minimize or restore")
		}
		c.Param("state", req.State)
		w, err = a.window.SetState(ctx, req.ID, state)
	}
	if err != nil {
		return nil, a.displayError(err)
	}
	return w, nil
}

// defaultCloseWait is the close_all wait_ms default (spec 6.4).
const defaultCloseWait = 3 * time.Second

func (a *API) windowCloseAll(c *server.Call) (any, error) {
	var req protocol.CloseAllRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	wait := defaultCloseWait
	if req.WaitMS != nil {
		if *req.WaitMS < 0 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "wait_ms must not be negative")
		}
		wait = time.Duration(*req.WaitMS) * time.Millisecond
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	self := []int{os.Getpid()}
	if a.supervised {
		self = append(self, os.Getppid())
	}
	res, err := a.window.CloseAll(c.Context(timeoutWindow), window.CloseAllOptions{
		ExcludeTitles: req.ExcludeTitles, ExcludePIDs: req.ExcludePIDs, Wait: wait, SelfPIDs: self,
	})
	if err != nil {
		return nil, a.displayError(err)
	}
	c.Param("closed", strconv.Itoa(len(res.Closed)))
	c.Param("remaining", strconv.Itoa(len(res.Remaining)))
	return res, nil
}

// queryBool parses an optional boolean query parameter.
func queryBool(c *server.Call, name string, def bool) (bool, error) {
	raw := c.Request.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, protocol.Errorf(protocol.CodeInvalidRequest, "%s must be true or false", name)
	}
	return v, nil
}

func (a *API) windowList(c *server.Call) (any, error) {
	visibleOnly, err := queryBool(c, "visible_only", true)
	if err != nil {
		return nil, err
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	windows, err := a.window.List(c.Context(timeoutWindow), visibleOnly)
	if err != nil {
		return nil, a.displayError(err)
	}
	if windows == nil {
		windows = []window.Window{}
	}
	return map[string]any{"windows": windows}, nil
}

func (a *API) windowActive(c *server.Call) (any, error) {
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	w, err := a.window.Active(c.Context(timeoutWindow))
	if err != nil {
		return nil, a.displayError(err)
	}
	if w == nil {
		return nil, protocol.Errorf(protocol.CodeNotFound, "no window is in the foreground")
	}
	return w, nil
}

func (a *API) decodeSelector(c *server.Call) (window.Selector, error) {
	var sel window.Selector
	if err := c.Decode(&sel); err != nil {
		return sel, err
	}
	if err := sel.Validate(); err != nil {
		return sel, err
	}
	if sel.ID != "" {
		c.Param("id", sel.ID)
	}
	if sel.Title != nil {
		c.Param("title", *sel.Title)
		c.Param("match", sel.Match)
	}
	return sel, nil
}

func (a *API) windowActivate(c *server.Call) (any, error) {
	sel, err := a.decodeSelector(c)
	if err != nil {
		return nil, err
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	w, err := a.window.Activate(c.Context(timeoutWindow), sel)
	if err != nil {
		return nil, a.displayError(err)
	}
	return w, nil
}

func (a *API) windowClose(c *server.Call) (any, error) {
	sel, err := a.decodeSelector(c)
	if err != nil {
		return nil, err
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	w, err := a.window.Close(c.Context(timeoutWindow), sel)
	if err != nil {
		return nil, a.displayError(err)
	}
	return w, nil
}
