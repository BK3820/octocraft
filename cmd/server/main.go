package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/strawhat-enterprise/octocraft/internal/factory"
)

func main() {
	app, err := factory.New(factory.Config{})
	if err != nil {
		log.Fatalf("initialize OctoCraft: %v", err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	addr := net.JoinHostPort("0.0.0.0", port)
	server := &http.Server{
		Addr:              addr,
		Handler:           app,
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
			log.Printf("graceful shutdown failed: %v", err)
		}
	}()

	log.Printf("OctoCraft running at http://%s (store: %s)", addr, app.StoreKind())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(fmt.Errorf("serve OctoCraft: %w", err))
	}
}
