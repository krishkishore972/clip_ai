// Package server composes the HTTP application independently of its CLI entrypoint.
package server

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

	"github.com/krishkishore972/clip_ai/internal/auth"
	"github.com/krishkishore972/clip_ai/internal/config"
	"github.com/krishkishore972/clip_ai/internal/db"
	"github.com/krishkishore972/clip_ai/internal/handler"
	"github.com/krishkishore972/clip_ai/internal/httpserver"
	"github.com/krishkishore972/clip_ai/internal/media"
	"github.com/krishkishore972/clip_ai/internal/preparation"
	"github.com/krishkishore972/clip_ai/internal/storage"
	"github.com/krishkishore972/clip_ai/internal/upload"
)

// Serve initializes dependencies and serves until shutdown.
func Serve(cfg config.Config, logger *slog.Logger, version string) error {
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

	authConfig, err := config.LoadAuth()
	if err != nil {
		return err
	}
	requireAuth := auth.New(authConfig, auth.Store{DB: database}, logger)
	uploads, err := NewDevelopmentUploads(context.Background(), cfg.Env, upload.SQLAssets{DB: database})
	if err != nil {
		return fmt.Errorf("initialize development uploads: %w", err)
	}
	if uploads != nil {
		logger.Warn("development S3 uploads enabled; validation requires the separately enabled worker")
	}
	preparationEnabled, err := config.LoadPreparationEnabled()
	if err != nil {
		return err
	}
	preparations := handler.NewMediaPreparationHandler(preparation.SQLStore{DB: database}, preparationEnabled, logger)
	srv := httpserver.NewServer(cfg, NewHandlerWithPreparation(logger, cfg.Env, uploads, media.SQLStatusReader{DB: database}, preparations, requireAuth))

	return run(srv, cfg.ShutdownTimeout, logger, version)
}

// NewDevelopmentUploads composes the opt-in, development-only S3 uploader.
func NewDevelopmentUploads(ctx context.Context, env string, assets upload.AssetRecorder) (handler.MultipartUploadService, error) {
	cfg, err := config.LoadDevelopmentUpload(env)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	store, err := storage.NewS3(ctx, cfg.S3)
	if err != nil {
		return nil, err
	}
	return upload.New(store, upload.Config{
		Assets: assets, Bucket: cfg.S3.Bucket,
		SigningSecret: []byte(cfg.SigningSecret), Scope: cfg.S3.Region + "/" + cfg.S3.Bucket,
		TokenTTL: cfg.TokenTTL, PresignTTL: cfg.S3.PresignTTL,
	})
}

// NewHandler composes public and authenticated routes, failing closed without auth.
func NewHandler(logger *slog.Logger, env string, uploads handler.MultipartUploadService, statuses handler.MediaStatusReader, authMiddleware ...func(http.Handler) http.Handler) http.Handler {
	return NewHandlerWithPreparation(logger, env, uploads, statuses, nil, authMiddleware...)
}

// NewHandlerWithPreparation adds private preparation routes while retaining the
// existing fail-closed authentication and development-only upload boundary.
func NewHandlerWithPreparation(logger *slog.Logger, env string, uploads handler.MultipartUploadService, statuses handler.MediaStatusReader, preparations *handler.MediaPreparationHandler, authMiddleware ...func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	protected := http.NewServeMux()
	protected.HandleFunc("GET /me", auth.Me)
	handler.NewMediaStatusHandler(statuses, logger).Register(protected)
	if preparations == nil {
		preparations = handler.NewMediaPreparationHandler(nil, false, logger)
	}
	preparations.Register(protected)

	mux.HandleFunc("GET /healthz", health)
	// Keep uploads development-only until media validation and release hardening
	// are ready, even though authentication now protects control requests.
	if env == "development" {
		handler.NewMultipartUploadHandler(uploads, logger).Register(protected)
	}
	// Fail closed even if a caller forgets to wire authentication.
	var private http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("WWW-Authenticate", "Bearer")
		_ = httpserver.WriteError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue.", nil)
	})
	if len(authMiddleware) == 1 && authMiddleware[0] != nil {
		private = authMiddleware[0](protected)
	}
	mux.Handle("/me", private)
	mux.Handle("/media/", private)
	if env == "development" {
		mux.Handle("/uploads", private)
		mux.Handle("/uploads/", private)
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

func run(srv *http.Server, shutdownTimeout time.Duration, logger *slog.Logger, version string) error {
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
