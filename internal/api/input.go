package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

func (a *API) inputRoutes() []*server.Route {
	single := []struct{ path, typ string }{
		{"/v1/input/mouse/move", input.TypeMouseMove},
		{"/v1/input/mouse/click", input.TypeMouseClick},
		{"/v1/input/mouse/down", input.TypeMouseDown},
		{"/v1/input/mouse/up", input.TypeMouseUp},
		{"/v1/input/mouse/drag", input.TypeMouseDrag},
		{"/v1/input/mouse/scroll", input.TypeMouseScroll},
		{"/v1/input/key/type", input.TypeKeyType},
		{"/v1/input/key/press", input.TypeKeyPress},
		{"/v1/input/key/down", input.TypeKeyDown},
		{"/v1/input/key/up", input.TypeKeyUp},
		{"/v1/input/key/hotkey", input.TypeKeyHotkey},
	}
	var routes []*server.Route
	for _, s := range single {
		routes = append(routes, &server.Route{Method: "POST", Path: s.path, Action: "input." + s.typ, Class: server.NonRetryable, Handler: a.inputSingle(s.typ)})
	}
	return append(routes, &server.Route{Method: "POST", Path: "/v1/input/batch", Action: "input.batch", Class: server.NonRetryable, Handler: a.inputBatch})
}

func (a *API) markInput() {
	now := a.now().UTC()
	a.lastInput.Store(&now)
}

func (a *API) inputSingle(typ string) server.HandlerFunc {
	return func(c *server.Call) (any, error) {
		var raw json.RawMessage
		if err := c.Decode(&raw); err != nil {
			return nil, err
		}
		act, err := input.Compile(typ, raw, c.Verbose())
		if err != nil {
			return nil, err
		}
		for k, v := range act.Params {
			c.Param(k, v)
		}
		// The deadline runs from arrival, so time spent queued behind other
		// input counts against it (spec 5.7).
		ctx := c.Context(act.DefaultTimeout())
		if err := a.requireDisplay(); err != nil {
			return nil, err
		}
		err = a.input.Run(ctx, func(ctx context.Context, x *input.Exec) error { return act.Run(ctx, x) })
		if err != nil {
			return nil, a.displayError(err)
		}
		a.markInput()
		return protocol.Empty{}, nil
	}
}

func (a *API) inputBatch(c *server.Call) (any, error) {
	var req protocol.BatchRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	batch, err := input.CompileBatch(req.Actions, c.Verbose())
	if err != nil {
		return nil, err
	}
	c.Param("actions", strconv.Itoa(len(batch.Actions)))
	ctx := c.Context(batch.DefaultTimeout())
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	executed := 0
	err = a.input.Run(ctx, func(ctx context.Context, x *input.Exec) error {
		var runErr error
		executed, runErr = batch.Run(ctx, x)
		return runErr
	})
	c.Param("executed", strconv.Itoa(executed))
	if executed > 0 {
		a.markInput()
	}
	if err != nil {
		perr := batchError(a.displayError(err))
		perr.WithDetail("executed", executed)
		if executed < len(batch.Actions) {
			perr.WithDetail("index", executed).WithDetail("type", batch.Actions[executed].Type)
		}
		return nil, perr
	}
	return protocol.BatchResult{Executed: executed}, nil
}

// batchError converts err to a *protocol.Error so batch details can be
// attached; context errors become timeouts (input is never retryable).
func batchError(err error) *protocol.Error {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return protocol.Errorf(protocol.CodeTimeout, "input batch timed out")
	case errors.Is(err, context.Canceled):
		return protocol.Errorf(protocol.CodeTimeout, "the client disconnected during the batch").WithReason(protocol.ReasonClientDisconnected)
	}
	return protocol.Errorf(protocol.CodeIOFailed, "%v", err)
}
