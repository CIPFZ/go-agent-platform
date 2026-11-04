// Package api binds the vm-agent action packages to HTTP routes: it decodes
// requests, applies the per-action default timeouts of spec section 7,
// gates display-bound actions on the session state, and fills in the audit
// parameter summaries of spec section 8.
package api

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/file"
	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/actions/window"
	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/config"
	"github.com/CIPFZ/go-agent-platform/internal/policy"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
)

// Deps are the platform-facing dependencies; tests substitute fakes.
type Deps struct {
	Config       *config.Config
	Audit        audit.Sink
	Screen       screen.Backend
	Input        input.Backend
	Window       window.Backend
	Session      func() session.State
	DPIAwareness string
	Policy       *policy.Policy
	Now          func() time.Time
}

// API holds the services behind the routes.
type API struct {
	cfg     *config.Config
	audit   audit.Sink
	now     func() time.Time
	started time.Time
	session func() session.State
	dpi     string
	policy  *policy.Policy

	screen     *screen.Service
	input      *input.Service
	window     *window.Service
	files      *file.FS
	downloader *file.Downloader

	lastInput atomic.Pointer[time.Time]
	statsMu   sync.Mutex
	stats     *server.Stats
}

// New builds the API with the platform backends.
func New(cfg *config.Config, sink audit.Sink) (*API, error) {
	pol, err := policy.Load(cfg.ExecAllowlist, cfg.ExecDenylist)
	if err != nil {
		return nil, err
	}
	// Command trees left by a crashed previous run are terminated before
	// serving (spec D4); the registry then tracks this run's trees.
	treeDir := filepath.Join(cfg.JobDir, ".trees")
	if killed := process.ReapOrphans(treeDir); len(killed) > 0 {
		sink.Record(audit.Event{Kind: "orphans_reaped", Details: map[string]string{"pids": fmt.Sprint(killed)}})
	}
	if err := process.TrackTrees(treeDir); err != nil {
		return nil, err
	}
	return NewWithDeps(Deps{
		Config:       cfg,
		Audit:        sink,
		Screen:       screen.NewBackend(),
		Input:        input.NewBackend(),
		Window:       window.NewBackend(),
		Session:      session.Detect,
		DPIAwareness: session.InitDPIAwareness(),
		Policy:       pol,
	})
}

// NewWithDeps builds the API from explicit dependencies.
func NewWithDeps(d Deps) (*API, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	files, err := file.New(d.Config.Roots)
	if err != nil {
		return nil, err
	}
	a := &API{
		cfg:     d.Config,
		audit:   d.Audit,
		now:     d.Now,
		started: d.Now(),
		session: d.Session,
		dpi:     d.DPIAwareness,
		policy:  d.Policy,
		screen:  screen.NewService(d.Screen),
		input:   input.NewService(d.Input),
		window:  window.NewService(d.Window),
		files:   files,

		downloader: file.NewDownloader(),
	}
	return a, nil
}

// SetStats connects the server counters used by /v1/metrics.
func (a *API) SetStats(s *server.Stats) {
	a.statsMu.Lock()
	a.stats = s
	a.statsMu.Unlock()
}

// Shutdown stops background activity (spec 4.4 steps 3 to 5).
func (a *API) Shutdown() {}

// Routes returns the route table.
func (a *API) Routes() []*server.Route {
	r := []*server.Route{
		{Method: "GET", Path: "/v1/probe", Action: "probe", Class: server.Retryable, Handler: a.probe},
		{Method: "GET", Path: "/v1/capabilities", Action: "capabilities", Class: server.Retryable, Handler: a.capabilities},

		{Method: "POST", Path: "/v1/screen/capture", Action: "screen.capture", Class: server.Retryable, Handler: a.screenCapture},
		{Method: "GET", Path: "/v1/screen/info", Action: "screen.info", Class: server.Retryable, Handler: a.screenInfo},
		{Method: "GET", Path: "/v1/screen/cursor", Action: "screen.cursor", Class: server.Retryable, Handler: a.screenCursor},
	}
	r = append(r, a.inputRoutes()...)
	r = append(r, a.windowRoutes()...)
	r = append(r, a.processRoutes()...)
	r = append(r, a.fileRoutes()...)
	return r
}

// requireDisplay returns no_display when the graphical session is missing or
// locked (spec section 9).
func (a *API) requireDisplay() error {
	st := a.session()
	if !st.DisplayReady || st.Locked {
		reason := "the graphical session is not available"
		if st.Locked {
			reason = "the session is locked"
		}
		return protocol.Errorf(protocol.CodeNoDisplay, "%s", reason)
	}
	return nil
}

func platformName() string { return runtime.GOOS }
