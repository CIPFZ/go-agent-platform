package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CIPFZ/go-agent-platform/internal/audit"
	"github.com/CIPFZ/go-agent-platform/internal/gateway"
)

func main() {
	addr := flag.String("addr", ":8081", "HTTP listen address")
	dataFile := flag.String("task-store", "", "durable task store JSON path")
	agentDataFile := flag.String("agent-store", "", "durable agent registry JSON path")
	auditFile := flag.String("audit-log", "", "append-only audit JSONL path")
	auditURL := flag.String("audit-url", "", "external audit collector URL")
	auditTokenFile := flag.String("audit-token-file", "", "file containing the external audit collector token")
	artifactDir := flag.String("artifact-dir", "", "durable artifact payload directory")
	authToken := flag.String("auth-token", "", "Bearer token required by API clients")
	authTokenFile := flag.String("auth-token-file", "", "file containing the current Bearer token; reread per request")
	tlsCert := flag.String("tls-cert", "", "PEM certificate path; enables HTTPS with tls-key")
	tlsKey := flag.String("tls-key", "", "PEM private key path; enables HTTPS with tls-cert")
	flag.Parse()
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("tls-cert and tls-key must be provided together")
	}
	var auditSinks audit.MultiSink
	if *auditFile != "" {
		sink, err := audit.NewFileSink(*auditFile)
		if err != nil {
			log.Fatal(err)
		}
		auditSinks = append(auditSinks, sink)
	}
	if *auditURL != "" {
		sink, err := audit.NewHTTPSink(*auditURL, *auditTokenFile)
		if err != nil {
			log.Fatal(err)
		}
		auditSinks = append(auditSinks, sink)
	}
	var auditSink audit.Sink
	if len(auditSinks) > 0 {
		auditSink = auditSinks
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           gateway.NewServer(gateway.Config{TaskPersistencePath: *dataFile, AgentPersistencePath: *agentDataFile, AuditSink: auditSink, ArtifactDir: *artifactDir, AuthToken: *authToken, AuthTokenFile: *authTokenFile}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("gateway listening on %s", *addr)
	go func() {
		var err error
		if *tlsCert != "" {
			err = server.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			err = server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Printf("gateway stopped: %v", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("gateway shutdown: %v", err)
	}
}
