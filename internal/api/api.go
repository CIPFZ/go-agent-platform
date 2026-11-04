// Package api binds the vm-agent action packages to HTTP routes: it decodes
// requests, applies the per-action default timeouts of spec section 7,
// gates display-bound actions on the session state, and fills in the audit
// parameter summaries of spec section 8.
package api

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/actions/clipboard"
	"github.com/CIPFZ/go-agent-platform/internal/actions/exec"
	"github.com/CIPFZ/go-agent-platform/internal/actions/file"
	"github.com/CIPFZ/go-agent-platform/internal/actions/input"
	"github.com/CIPFZ/go-agent-platform/internal/actions/process"
	"github.com/CIPFZ/go-agent-platform/internal/actions/record"
	"github.com/CIPFZ/go-agent-platform/internal/actions/screen"
	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/actions/window"
	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/config"
	"github.com/CIPFZ/go-agent-platform/internal/policy"
	"github.com/CIPFZ/go-agent-platform/internal/protocol"
	"github.com/CIPFZ/go-agent-platform/internal/server"
	"github.com/CIPFZ/go-agent-platform/internal/supervisor"
)

// Deps are the platform-facing dependencies; tests substitute fakes.
type Deps struct {
	Config    *config.Config
	Audit     audit.Sink
	Screen    screen.Backend
	Input     input.Backend
	Window    window.Backend
	Clipboard clipboard.Backend
	// FFmpeg is the resolved ffmpeg executable; empty disables recording.
	FFmpeg string
	// RecordGrabArgs overrides ffmpeg's input arguments (tests).
	RecordGrabArgs func(fps int, r protocol.Rect) ([]string, error)
	Session        func() session.State
	DPIAwareness   string
	Policy         *policy.Policy
	Now            func() time.Time
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
	clipboard  *clipboard.Service
	files      *file.FS
	downloader *file.Downloader
	jobs       *exec.Jobs
	recorder   *record.Recorder

	stopJanitor chan struct{}
	janitorDone chan struct{}

	// supervised is true when a -supervise parent started this process, so
	// its pid is protected from process/kill.
	supervised bool

	shutdownOnce sync.Once
	lastInput    atomic.Pointer[time.Time]
	statsMu      sync.Mutex
	stats        *server.Stats
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
	ffmpeg, ffErr := record.FindFFmpeg(cfg.FFmpeg)
	if ffErr != nil {
		log.Printf("vm-agent: recording unavailable: %v", ffErr)
	}
	return NewWithDeps(Deps{
		FFmpeg:       ffmpeg,
		Config:       cfg,
		Audit:        sink,
		Screen:       screen.NewBackend(),
		Input:        input.NewBackend(),
		Window:       window.NewBackend(),
		Clipboard:    clipboard.NewBackend(),
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
	jobs, err := exec.OpenJobs(d.Config.JobDir, d.Config.JobRetention, d.Now)
	if err != nil {
		return nil, err
	}
	recorder, err := record.Open(d.Config.RecordDir, d.FFmpeg, d.Config.RecordQuota, d.Config.RecordRetention, d.Now)
	if err != nil {
		return nil, err
	}
	if d.RecordGrabArgs != nil {
		recorder.SetGrabArgs(d.RecordGrabArgs)
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

		clipboard: clipboard.NewService(d.Clipboard),
		files:     files,

		downloader: file.NewDownloader(),
		supervised: os.Getenv(supervisor.EnvSupervised) == "1",
		jobs:       jobs,
		recorder:   recorder,

		stopJanitor: make(chan struct{}),
		janitorDone: make(chan struct{}),
	}
	go a.janitor()
	return a, nil
}

// SetStats connects the server counters used by /v1/metrics.
func (a *API) SetStats(s *server.Stats) {
	a.statsMu.Lock()
	a.stats = s
	a.statsMu.Unlock()
}

// janitorInterval is how often retention limits are applied.
const janitorInterval = time.Minute

// janitor applies the job and recording retention and quota (spec 6.5,
// 6.8) in the background.
func (a *API) janitor() {
	defer close(a.janitorDone)
	t := time.NewTicker(janitorInterval)
	defer t.Stop()
	for {
		select {
		case <-a.stopJanitor:
			return
		case <-t.C:
			a.jobs.Prune()
			a.recorder.Prune()
		}
	}
}

// Shutdown stops background activity in the order of spec 4.4: an active
// recording is stopped with "q" (5 s limit) so its file is complete, then
// running background jobs are killed (reason agent_shutdown) and their
// records written; process trees die with them.
func (a *API) Shutdown() {
	a.shutdownOnce.Do(func() {
		close(a.stopJanitor)
		<-a.janitorDone
		a.recorder.Shutdown() // step 3: finish recordings cleanly
		a.jobs.Shutdown()     // step 4: kill background jobs
	})
}

// Routes returns the route table.
func (a *API) Routes() []*server.Route {
	r := []*server.Route{
		{Method: "GET", Path: "/v1/probe", Action: "probe", Class: server.Retryable, Handler: a.probe},
		{Method: "GET", Path: "/v1/capabilities", Action: "capabilities", Class: server.Retryable, Handler: a.capabilities},
		{Method: "GET", Path: "/v1/metrics", Action: "metrics", Class: server.Retryable, Handler: a.metrics},

		{Method: "POST", Path: "/v1/screen/capture", Action: "screen.capture", Class: server.Retryable, Handler: a.screenCapture},
		{Method: "GET", Path: "/v1/screen/info", Action: "screen.info", Class: server.Retryable, Handler: a.screenInfo},
		{Method: "GET", Path: "/v1/screen/cursor", Action: "screen.cursor", Class: server.Retryable, Handler: a.screenCursor},
	}
	r = append(r, a.inputRoutes()...)
	r = append(r, a.windowRoutes()...)
	r = append(r, a.processRoutes()...)
	r = append(r, a.jobRoutes()...)
	r = append(r, a.clipboardRoutes()...)
	r = append(r, a.recordRoutes()...)
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
