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

	"github.com/CIPFZ/go-agent-platform/internal/gateway"
)

func main() {
	addr := flag.String("addr", ":8081", "HTTP listen address")
	flag.Parse()

	server := &http.Server{
		Addr:              *addr,
		Handler:           gateway.NewServer(gateway.Config{}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("gateway listening on %s", *addr)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
