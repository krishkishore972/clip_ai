// Command server is the HTTP entrypoint for the API.
//
// It loads configuration, builds the route table, and owns the process
// lifecycle. Feature logic belongs in internal packages, not here.
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
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/handler"
	"github.com/krishkishore972/clip_ai/internal/httpserver"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/upload"
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

	if err := serve(cfg, logger); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func serve(cfg config.Config, logger *slog.Logger) error {
	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		// Driver errors may include the DSN or database credentials.
		return errors.New("database connection failed; check DATABASE_URL and PostgreSQL availability")
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("database pool close failed")
		}
	}()
	logger.Info("database connected")

	uploads, err := developmentUploads(context.Background(), cfg.Env)
	if err != nil {
		return fmt.Errorf("initialize development uploads: %w", err)
	}
	if uploads != nil {
		logger.Warn("development S3 uploads enabled without user authentication; do not expose publicly")
	}
	srv := httpserver.NewServer(cfg, newHandler(logger, cfg.Env, uploads))

	return run(srv, cfg.ShutdownTimeout, logger)
}

func developmentUploads(ctx context.Context, env string) (handler.MultipartUploadService, error) {
	cfg, err := config.LoadDevelopmentUpload(env)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	store, err := storage.NewS3(ctx, cfg.S3)
	if err != nil {
		return nil, err
	}
	return upload.New(store, upload.Config{
		SigningSecret: []byte(cfg.SigningSecret), Scope: cfg.S3.Region + "/" + cfg.S3.Bucket,
		TokenTTL: cfg.TokenTTL, PresignTTL: cfg.S3.PresignTTL,
	})
}

// newHandler builds the route table and installs middleware. Add routes here
// as features land; middleware order is outermost-first.
func newHandler(logger *slog.Logger, env string, uploads handler.MultipartUploadService) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", health)
	// Temporary development routes. Do not enable outside development until
	// authentication and server-derived session ownership are implemented.
	if env == "development" {
		handler.NewMultipartUploadHandler(uploads, logger).Register(mux)
	}

	return httpserver.Chain(mux,
		httpserver.RequestID,
		httpserver.LogRequests(logger),
		httpserver.Recover(logger),
	)
}

// health reports process liveness only. It deliberately does not check
// dependencies; a readiness endpoint will do that once the DB and queue exist.
func health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// run serves until SIGINT/SIGTERM, then drains in-flight requests before returning.
/*
Start listening
      ↓
Handle requests
      ↓
Wait for a server error or shutdown signal
      ↓
Stop accepting new requests
      ↓
Let active requests finish within a timeout
      ↓
Return
*/

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
