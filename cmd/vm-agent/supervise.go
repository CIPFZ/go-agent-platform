package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/CIPFZ/go-agent-platform/internal/config"
	"github.com/CIPFZ/go-agent-platform/internal/supervisor"
)

// supervise runs the -supervise parent (spec 4.1) and returns the exit code.
func supervise(cfg *config.Config, args []string) int {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("vm-agent supervisor: %v", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stateDir := filepath.Dir(cfg.JobDir)
	opts := supervisor.Defaults(exe, config.ChildArgs(args), filepath.Join(stateDir, "vm-agent.lock"))
	opts.LogPath = filepath.Join(stateDir, "vm-agent.log")
	err = supervisor.Run(ctx, opts)
	if errors.Is(err, supervisor.ErrAlreadyRunning) {
		log.Printf("vm-agent supervisor: %v; exiting", err)
		return 0
	}
	if err != nil {
		log.Printf("vm-agent supervisor: %v", err)
		return 1
	}
	return 0
}

// watchSupervisor makes a supervised child shut down when the supervisor
// closes its stdin.
func watchSupervisor(stop func()) func() {
	supervisor.WatchStdin(stop)
	return func() {}
}

func itoa(n int) string { return strconv.Itoa(n) }
