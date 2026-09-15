package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/east-true/docker-log-viewer/internal/dockerengine"
	"github.com/east-true/docker-log-viewer/internal/web"
)

func main() {
	listenDefault := os.Getenv("DOCKER_LOG_VIEWER_LISTEN")
	if listenDefault == "" {
		listenDefault = "127.0.0.1:8080"
	}
	listen := flag.String("listen", listenDefault, "HTTP listen address")
	flag.Parse()

	engine, err := dockerengine.Open()
	if err != nil {
		log.Fatalf("open Docker Engine client: %v", err)
	}
	defer engine.Close()

	handler, err := web.New(engine)
	if err != nil {
		log.Fatalf("create web handler: %v", err)
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown: %v", err)
		}
	}()

	log.Printf("docker-log-viewer listening on http://%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve HTTP: %v", err)
	}
}
