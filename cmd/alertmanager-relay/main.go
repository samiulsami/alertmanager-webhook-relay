package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.openviz.dev/alertmanager-relay/internal/config"
	"go.openviz.dev/alertmanager-relay/internal/server"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	handler := server.New(cfg)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown failed", "error", err)
		}
	}()

	slog.Info("starting alertmanager relay", "addr", cfg.ListenAddr, "request_timeout", cfg.RequestTimeout, "send_resolved", cfg.SendResolved)
	err = httpServer.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		slog.Info("server stopped")
	}
}
