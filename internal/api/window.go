package api

import (
	"strconv"

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
	}
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
