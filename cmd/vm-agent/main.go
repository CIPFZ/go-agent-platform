// Command vm-agent is the in-VM desktop automation service described by
// docs/spec.md.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/CIPFZ/go-agent-platform/internal/actions/a11y"
	"github.com/CIPFZ/go-agent-platform/internal/actions/file"
	"github.com/CIPFZ/go-agent-platform/internal/actions/session"
	"github.com/CIPFZ/go-agent-platform/internal/api"
	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/config"
	"github.com/CIPFZ/go-agent-platform/internal/dedup"
	"github.com/CIPFZ/go-agent-platform/internal/server"
	"github.com/CIPFZ/go-agent-platform/internal/version"
)

func main() {
	// The Linux accessibility helper is vm-agent re-executed with a request
	// in its environment (spec 6.9: a time-limited child process).
	if a11y.IsHelper() {
		os.Exit(a11y.HelperMain())
	}
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if cfg.Supervise {
		os.Exit(supervise(cfg, os.Args[1:]))
	}
	// One-time approval of the portal RemoteDesktop fallback for Wayland
	// input; the token is kept next to the lock file.
	if os.Getenv("VM_AGENT_PORTAL_SETUP") == "1" {
		os.Exit(session.PortalSetup(filepath.Dir(cfg.JobDir)))
	}
	// Spec 4.1: declare Per-Monitor V2 DPI awareness before anything else
	// touches the desktop (no-op on Linux).
	session.InitDPIAwareness()
	if err := run(cfg); err != nil {
		log.Printf("vm-agent: %v", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config) error {
	sink, err := audit.NewRotatingFileSink(cfg.AuditLog, cfg.AuditMaxSize, cfg.AuditMaxFiles)
	if err != nil {
		return err
	}
	agent, err := api.New(cfg, sink)
	if err != nil {
		return err
	}
	var cache *dedup.Cache
	if cfg.DedupWindow > 0 {
		cache = dedup.New(cfg.DedupWindow, dedup.DefaultCapacity, nil)
	}
	srv := server.New(server.Options{
		Audit:          sink,
		Dedup:          cache,
		MaxConcurrency: cfg.MaxConcurrency,
		Authenticate:   server.Passthrough,
		AuditVerbose:   cfg.AuditVerbose,
	}, agent.Routes())
	agent.SetStats(srv.Stats())
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	httpServer := server.NewHTTPServer(cfg.Listen, srv.Handler())
	sink.Record(audit.Event{Kind: "startup", Details: map[string]string{"version": version.Version, "listen": listener.Addr().String(), "pid": itoa(os.Getpid())}})
	// Spec section 7: the free-space floor is also checked at startup; a low
	// volume is reported but does not stop the service, since only writing
	// actions are refused.
	for _, dir := range []string{cfg.JobDir, cfg.RecordDir} {
		if err := file.CheckFreeSpace(dir); err != nil {
			log.Printf("vm-agent: %v", err)
			sink.Record(audit.Event{Kind: "disk_low", Details: map[string]string{"path": dir, "error": err.Error()}})
		}
	}
	log.Printf("vm-agent %s listening on %s", version.Version, listener.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopSupervised := watchSupervisor(stop)
	defer stopSupervised()
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			agent.Shutdown()
			return err
		}
	case <-ctx.Done():
	}

	// Spec 4.4: stop accepting, wait for in-flight requests up to 10 s and
	// abort the rest, then let the actions finish recordings, kill jobs and
	// release Job handles.
	log.Printf("vm-agent shutting down")
	if aborted, unfinished := server.GracefulStop(httpServer, srv, server.ShutdownGrace, server.AbortWait); aborted > 0 {
		log.Printf("vm-agent: aborted %d requests still running after %s; %d did not finish within %s", aborted, server.ShutdownGrace, unfinished, server.AbortWait)
	}
	agent.Shutdown()
	sink.Record(audit.Event{Kind: "shutdown", Details: map[string]string{"version": version.Version}})
	return nil
}
