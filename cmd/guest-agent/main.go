package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/agent"
)

func main() {
	gateway := flag.String("gateway", "http://127.0.0.1:8081", "gateway base URL")
	agentID := flag.String("agent-id", "", "stable Agent identity; defaults to the current hostname")
	stateFile := flag.String("state-file", "", "file used to persist a generated Agent identity")
	agentVersion := flag.String("agent-version", "dev", "guest-agent version")
	interval := flag.Duration("heartbeat-interval", 15*time.Second, "heartbeat interval")
	workspace := flag.String("workspace", "/var/lib/guest-agent/workspace", "sandbox workspace for task execution")
	authToken := flag.String("auth-token", "", "Bearer token for Gateway API")
	flag.Parse()
	if strings.TrimSpace(*agentID) == "" {
		if strings.TrimSpace(*stateFile) != "" {
			persistedID, err := agent.LoadOrCreateID(*stateFile)
			if err != nil {
				log.Fatal(err)
			}
			*agentID = persistedID
		} else {
			hostname, err := os.Hostname()
			if err != nil {
				hostname = "unknown-agent"
			}
			*agentID = hostname
		}
	}
	hostname, _ := os.Hostname()
	client, err := agent.NewClient(agent.Config{
		GatewayURL:        *gateway,
		AgentID:           *agentID,
		AgentVersion:      *agentVersion,
		Hostname:          hostname,
		HeartbeatInterval: *interval,
		AuthToken:         *authToken,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*workspace, 0o700); err != nil {
		log.Fatalf("create workspace: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("guest-agent connecting to %s as %s", *gateway, *agentID)
	runner := &agent.Runner{Client: client, HeartbeatInterval: *interval, Executor: &agent.Executor{Workspace: *workspace}}
	if err := runner.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
	log.Printf("guest-agent stopped")
}
