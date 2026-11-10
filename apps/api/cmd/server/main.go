// Command server is the HTTP entrypoint for the API.
//
// It wires configuration, routing, and process lifecycle only. Feature logic
// belongs in internal packages, not here.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/krishkishore972/clip_ai/internal/config"
)

// version is stamped at build time via -ldflags "-X main.version=<git describe>".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		// The logger is not configured yet; report to stderr and stop.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))

	srv := newServer(cfg)

	if err := run(srv, cfg.ShutdownTimeout, logger); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func newServer(cfg config.Config) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

// run serves until SIGINT/SIGTERM, then drains in-flight requests before returning.
func run(srv *http.Server, shutdownTimeout time.Duration, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	logger.Info("server listening", "addr", ln.Addr().String(), "version", version)

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	logger.Info("shutdown requested", "addr", srv.Addr)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return err
	}

	logger.Info("server stopped", "addr", srv.Addr)
	return nil
}
