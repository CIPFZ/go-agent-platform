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
	authToken := flag.String("auth-token", "", "Bearer token required by API clients")
	tlsCert := flag.String("tls-cert", "", "PEM certificate path; enables HTTPS with tls-key")
	tlsKey := flag.String("tls-key", "", "PEM private key path; enables HTTPS with tls-cert")
	flag.Parse()
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("tls-cert and tls-key must be provided together")
	}
	var auditSink audit.Sink
	if *auditFile != "" {
		sink, err := audit.NewFileSink(*auditFile)
		if err != nil {
			log.Fatal(err)
		}
		auditSink = sink
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           gateway.NewServer(gateway.Config{TaskPersistencePath: *dataFile, AgentPersistencePath: *agentDataFile, AuditSink: auditSink, AuthToken: *authToken}).Handler(),
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
