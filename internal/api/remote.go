package api

import (
	"bytes"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/remote"
	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

// timeoutRemote bounds opening a remote session: the offer is answered once
// this side's addresses are gathered, which needs no network round trip.
const timeoutRemote = 10 * time.Second

func (a *API) remoteRoutes() []*server.Route {
	return []*server.Route{
		{Method: "GET", Path: "/remote/", Action: "remote.page", Class: server.Retryable, Handler: a.remotePage},
		{Method: "POST", Path: "/v1/remote/sessions", Action: "remote.open", Class: server.NonRetryable, Handler: a.remoteOpen},
		{Method: "GET", Path: "/v1/remote/sessions", Action: "remote.list", Class: server.Retryable, Handler: a.remoteList},
		{Method: "DELETE", Path: "/v1/remote/sessions/{session_id}", Action: "remote.close", Class: server.NonRetryable, Handler: a.remoteClose},
	}
}

// newRemote builds the remote desktop when -remote is set.
func (a *API) newRemote(d Deps) *remote.Manager {
	if !d.Config.Remote {
		return nil
	}
	return remote.New(remote.Options{
		FFmpeg:      d.FFmpeg,
		UDPPort:     d.Config.RemoteUDPPort,
		IdleRelease: d.Config.RemoteIdleRelease,
		Input:       a.input,
		Screen:      a.screen,
		Clipboard:   a.clipboard,
		Desktop:     func() remote.Desktop { return remote.Desktop{Locked: a.session().Locked} },
		Source:      d.RemoteSource,
		Loopback:    d.RemoteLoopback,
		Now:         d.Now,
		OnInput:     a.markInput,
		OnEvent: func(kind string, details map[string]string) {
			a.audit.Record(audit.Event{Kind: kind, Details: details})
		},
	})
}

func (a *API) remoteEnabled() error {
	if a.remote == nil {
		return protocol.Unsupported("the remote desktop is not enabled; start vm-agent with -remote")
	}
	return a.remote.Available()
}

// remoteInUse refuses the input API while a person holds the desktop: two
// sources moving the same pointer and keyboard would break each other.
func (a *API) remoteInUse() error {
	if a.remote == nil {
		return nil
	}
	return a.remote.InUse()
}

func (a *API) remotePage(c *server.Call) (any, error) {
	_ = c.Context(timeoutSystem)
	if err := a.remoteEnabled(); err != nil {
		return nil, err
	}
	return &server.Stream{ContentType: "text/html; charset=utf-8", Size: int64(len(remote.Page)),
		Header: map[string]string{"Cache-Control": "no-store"}, Body: bytes.NewReader(remote.Page)}, nil
}

func (a *API) remoteOpen(c *server.Call) (any, error) {
	var req protocol.RemoteOpenRequest
	if err := c.Decode(&req); err != nil {
		return nil, err
	}
	c.Param("holder", req.Holder)
	ctx := c.Context(timeoutRemote)
	if err := a.remoteEnabled(); err != nil {
		return nil, err
	}
	if err := a.requireDisplay(); err != nil {
		return nil, err
	}
	res, err := a.remote.Open(ctx, remote.OpenRequest{SDP: req.SDP, Holder: req.Holder, ClientID: req.ClientID, FPS: req.FPS,
		BitrateKbps: req.BitrateKbps, Display: req.Display, RemoteAddr: c.Request.RemoteAddr})
	if err != nil {
		return nil, a.displayError(err)
	}
	c.Param("session_id", res.SessionID)
	return res, nil
}

func (a *API) remoteList(c *server.Call) (any, error) {
	_ = c.Context(timeoutSystem)
	sessions := []remote.Info{}
	if a.remote != nil {
		sessions = a.remote.Sessions()
	}
	return map[string]any{"sessions": sessions}, nil
}

func (a *API) remoteClose(c *server.Call) (any, error) {
	id := c.Request.PathValue("session_id")
	c.Param("session_id", id)
	_ = c.Context(timeoutRemote)
	if err := a.remoteEnabled(); err != nil {
		return nil, err
	}
	if err := a.remote.Close(id); err != nil {
		return nil, err
	}
	return protocol.Empty{}, nil
}
